import difflib
import subprocess
from dataclasses import dataclass
from pathlib import Path

from fastapi import HTTPException

from .files import normalize_relative, resolve_path, served_root
from .models import GitCommitRequest, GitDiffFile, GitDiffText, GitStatus


@dataclass(frozen=True)
class GitContext:
    cwd: Path
    scope_path: str


def _run_git(args: list[str], *, check: bool = True, cwd: Path | None = None, user_id: str | None = None) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        ["git", *args],
        cwd=cwd or served_root(user_id),
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if check and result.returncode != 0:
        message = result.stderr.strip() or result.stdout.strip() or "Git command failed"
        raise HTTPException(status_code=400, detail=message)
    return result


def _scope_start(scope: str | None, user_id: str | None = None) -> Path:
    if not scope:
        return served_root(user_id)
    target = resolve_path(scope, user_id)
    if target.exists():
        return target if target.is_dir() else target.parent
    start = target.parent
    root = served_root(user_id)
    while not start.exists() and start != root and start.is_relative_to(root):
        start = start.parent
    return start


def _discover_single_child_repo(user_id: str | None = None) -> Path | None:
    try:
        children = list(served_root(user_id).iterdir())
    except OSError:
        return None
    repos = [child for child in children if child.is_dir() and child.joinpath(".git").exists()]
    return repos[0] if len(repos) == 1 else None


def _git_context(scope: str | None = None, user_id: str | None = None) -> GitContext:
    start = _scope_start(scope, user_id)
    result = _run_git(["rev-parse", "--show-toplevel"], check=False, cwd=start)
    if result.returncode != 0 and not scope:
        child_repo = _discover_single_child_repo(user_id)
        if child_repo is not None:
            start = child_repo
            result = _run_git(["rev-parse", "--show-toplevel"], check=False, cwd=child_repo)
    if result.returncode != 0:
        raise HTTPException(status_code=409, detail="Current folder is not inside a Git repository")

    repo_root = Path(result.stdout.strip()).resolve()
    root = served_root(user_id)
    if not (repo_root == root or repo_root.is_relative_to(root) or root.is_relative_to(repo_root)):
        raise HTTPException(status_code=409, detail="Current folder is not inside a Git repository")
    try:
        scope_path = start.resolve(strict=False).relative_to(repo_root).as_posix()
    except ValueError:
        scope_path = "."
    return GitContext(cwd=repo_root, scope_path=scope_path or ".")


def _git_path(ctx: GitContext, path: str, user_id: str | None = None) -> str:
    target = resolve_path(path, user_id).resolve(strict=False)
    try:
        return target.relative_to(ctx.cwd).as_posix()
    except ValueError as exc:
        raise HTTPException(status_code=400, detail="Path is outside the active Git repository") from exc


def _served_path(ctx: GitContext, git_path: str, user_id: str | None = None) -> str:
    target = ctx.cwd.joinpath(git_path).resolve(strict=False)
    try:
        return target.relative_to(served_root(user_id)).as_posix()
    except ValueError:
        return git_path


def _is_binary_path(ctx: GitContext, path: str) -> bool:
    target = ctx.cwd / path
    if not target.exists() or not target.is_file():
        return False
    chunk = target.read_bytes()[:8192]
    return b"\0" in chunk


def _untracked_added_lines(ctx: GitContext, path: str) -> int:
    target = ctx.cwd / path
    try:
        return len(target.read_text(encoding="utf-8", errors="replace").splitlines())
    except OSError:
        return 0


def _numstat_for(ctx: GitContext, path: str, status: str) -> tuple[int | None, int | None, bool]:
    if status == "??":
        is_binary = _is_binary_path(ctx, path)
        return (None, None, True) if is_binary else (_untracked_added_lines(ctx, path), 0, False)

    result = _run_git(["diff", "--numstat", "HEAD", "--", path], check=False, cwd=ctx.cwd)
    for line in result.stdout.splitlines():
        parts = line.split("\t")
        if len(parts) < 3:
            continue
        added_raw, deleted_raw = parts[0], parts[1]
        if added_raw == "-" or deleted_raw == "-":
            return None, None, True
        return int(added_raw), int(deleted_raw), False
    return 0, 0, _is_binary_path(ctx, path)


def _exists_in_head(ctx: GitContext, path: str) -> bool:
    result = _run_git(["cat-file", "-e", f"HEAD:{path}"], check=False, cwd=ctx.cwd)
    return result.returncode == 0


def git_status(scope: str | None = None, user_id: str | None = None) -> GitStatus:
    ctx = _git_context(scope, user_id)
    result = _run_git(["status", "--porcelain=v1", "-z", "--untracked-files=all", "--", ctx.scope_path], cwd=ctx.cwd)
    raw_entries = [entry for entry in result.stdout.split("\0") if entry]
    files: list[GitDiffFile] = []
    index = 0
    while index < len(raw_entries):
        entry = raw_entries[index]
        status = entry[:2]
        path = entry[3:]
        if "R" in status or "C" in status:
            index += 1
        path = normalize_relative(path)
        added, deleted, is_binary = _numstat_for(ctx, path, status)
        files.append(GitDiffFile(path=_served_path(ctx, path, user_id), status=status, added=added, deleted=deleted, is_binary=is_binary))
        index += 1
    files.sort(key=lambda item: item.path)
    return GitStatus(files=files)


def _untracked_diff(ctx: GitContext, path: str, served_path: str) -> str:
    target = ctx.cwd / path
    try:
        lines = target.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    return "\n".join(
        difflib.unified_diff(
            [],
            lines,
            fromfile="/dev/null",
            tofile=f"b/{served_path}",
            lineterm="",
        )
    ) + "\n"


def git_diff(path: str, user_id: str | None = None) -> GitDiffText:
    normalized = normalize_relative(path)
    ctx = _git_context(normalized, user_id)
    git_path = _git_path(ctx, normalized, user_id)
    status = next((item.status for item in git_status(normalized, user_id).files if item.path == normalized), "")
    if not status:
        raise HTTPException(status_code=404, detail="File has no Git changes")
    if _is_binary_path(ctx, git_path):
        return GitDiffText(path=normalized, diff="", is_binary=True)
    if status == "??":
        return GitDiffText(path=normalized, diff=_untracked_diff(ctx, git_path, normalized), is_binary=False)
    result = _run_git(["diff", "--no-ext-diff", "--find-renames", "HEAD", "--", git_path], check=False, cwd=ctx.cwd)
    if result.returncode not in (0, 1):
        message = result.stderr.strip() or "Unable to read diff"
        raise HTTPException(status_code=400, detail=message)
    if "Binary files " in result.stdout:
        return GitDiffText(path=normalized, diff="", is_binary=True)
    return GitDiffText(path=normalized, diff=result.stdout, is_binary=False)


def git_stage(path: str | None = None, scope: str | None = None, user_id: str | None = None) -> GitStatus:
    ctx = _git_context(path or scope, user_id)
    args = ["add"]
    args.extend(["--", _git_path(ctx, normalize_relative(path), user_id)] if path else ["--all", "--", ctx.scope_path])
    _run_git(args, cwd=ctx.cwd)
    return git_status(path or scope, user_id)


def git_revert(path: str, user_id: str | None = None) -> GitStatus:
    normalized = normalize_relative(path)
    ctx = _git_context(normalized, user_id)
    git_path = _git_path(ctx, normalized, user_id)
    status = next((item.status for item in git_status(normalized, user_id).files if item.path == normalized), "")
    if not status:
        raise HTTPException(status_code=404, detail="File has no Git changes")
    if status == "??":
        target = resolve_path(normalized, user_id)
        if target.is_dir():
            raise HTTPException(status_code=400, detail="Refusing to remove untracked directory")
        target.unlink(missing_ok=True)
    elif not _exists_in_head(ctx, git_path):
        _run_git(["rm", "-f", "--", git_path], cwd=ctx.cwd)
    else:
        _run_git(["restore", "--source=HEAD", "--staged", "--worktree", "--", git_path], cwd=ctx.cwd)
    return git_status(normalized, user_id)


def git_commit(request: GitCommitRequest, user_id: str | None = None) -> GitStatus:
    ctx = _git_context(request.scope, user_id)
    message = request.message.strip()
    if not message:
        raise HTTPException(status_code=400, detail="Commit message is required")
    _run_git(["commit", "-m", message], cwd=ctx.cwd)
    return git_status(request.scope, user_id)


def git_push(scope: str | None = None, user_id: str | None = None) -> dict[str, str]:
    ctx = _git_context(scope, user_id)
    result = _run_git(["push"], cwd=ctx.cwd)
    output = (result.stdout + result.stderr).strip()
    return {"status": "ok", "output": output}
