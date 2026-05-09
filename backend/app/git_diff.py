import difflib
import subprocess

from fastapi import HTTPException

from .config import settings
from .files import normalize_relative, resolve_path
from .models import GitCommitRequest, GitDiffFile, GitDiffText, GitStatus


def _run_git(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        ["git", *args],
        cwd=settings.root_resolved,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if check and result.returncode != 0:
        message = result.stderr.strip() or result.stdout.strip() or "Git command failed"
        raise HTTPException(status_code=400, detail=message)
    return result


def _ensure_repo() -> None:
    result = _run_git(["rev-parse", "--is-inside-work-tree"], check=False)
    if result.returncode != 0 or result.stdout.strip() != "true":
        raise HTTPException(status_code=409, detail="Current folder is not inside a Git repository")


def _is_binary_path(path: str) -> bool:
    target = resolve_path(path)
    if not target.exists() or not target.is_file():
        return False
    chunk = target.read_bytes()[:8192]
    return b"\0" in chunk


def _untracked_added_lines(path: str) -> int:
    target = resolve_path(path)
    try:
        return len(target.read_text(encoding="utf-8", errors="replace").splitlines())
    except OSError:
        return 0


def _numstat_for(path: str, status: str) -> tuple[int | None, int | None, bool]:
    if status == "??":
        is_binary = _is_binary_path(path)
        return (None, None, True) if is_binary else (_untracked_added_lines(path), 0, False)

    result = _run_git(["diff", "--numstat", "HEAD", "--", path], check=False)
    for line in result.stdout.splitlines():
        parts = line.split("\t")
        if len(parts) < 3:
            continue
        added_raw, deleted_raw = parts[0], parts[1]
        if added_raw == "-" or deleted_raw == "-":
            return None, None, True
        return int(added_raw), int(deleted_raw), False
    return 0, 0, _is_binary_path(path)


def _exists_in_head(path: str) -> bool:
    result = _run_git(["cat-file", "-e", f"HEAD:{path}"], check=False)
    return result.returncode == 0


def git_status() -> GitStatus:
    _ensure_repo()
    result = _run_git(["status", "--porcelain=v1", "-z", "--untracked-files=all"])
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
        added, deleted, is_binary = _numstat_for(path, status)
        files.append(GitDiffFile(path=path, status=status, added=added, deleted=deleted, is_binary=is_binary))
        index += 1
    files.sort(key=lambda item: item.path)
    return GitStatus(files=files)


def _untracked_diff(path: str) -> str:
    target = resolve_path(path)
    try:
        lines = target.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    return "\n".join(
        difflib.unified_diff(
            [],
            lines,
            fromfile="/dev/null",
            tofile=f"b/{path}",
            lineterm="",
        )
    ) + "\n"


def git_diff(path: str) -> GitDiffText:
    _ensure_repo()
    normalized = normalize_relative(path)
    status = next((item.status for item in git_status().files if item.path == normalized), "")
    if not status:
        raise HTTPException(status_code=404, detail="File has no Git changes")
    if _is_binary_path(normalized):
        return GitDiffText(path=normalized, diff="", is_binary=True)
    if status == "??":
        return GitDiffText(path=normalized, diff=_untracked_diff(normalized), is_binary=False)
    result = _run_git(["diff", "--no-ext-diff", "--find-renames", "HEAD", "--", normalized], check=False)
    if result.returncode not in (0, 1):
        message = result.stderr.strip() or "Unable to read diff"
        raise HTTPException(status_code=400, detail=message)
    if "Binary files " in result.stdout:
        return GitDiffText(path=normalized, diff="", is_binary=True)
    return GitDiffText(path=normalized, diff=result.stdout, is_binary=False)


def git_stage(path: str | None = None) -> GitStatus:
    _ensure_repo()
    args = ["add"]
    args.extend(["--", normalize_relative(path)] if path else ["--all"])
    _run_git(args)
    return git_status()


def git_revert(path: str) -> GitStatus:
    _ensure_repo()
    normalized = normalize_relative(path)
    status = next((item.status for item in git_status().files if item.path == normalized), "")
    if not status:
        raise HTTPException(status_code=404, detail="File has no Git changes")
    if status == "??":
        target = resolve_path(normalized)
        if target.is_dir():
            raise HTTPException(status_code=400, detail="Refusing to remove untracked directory")
        target.unlink(missing_ok=True)
    elif not _exists_in_head(normalized):
        _run_git(["rm", "-f", "--", normalized])
    else:
        _run_git(["restore", "--source=HEAD", "--staged", "--worktree", "--", normalized])
    return git_status()


def git_commit(request: GitCommitRequest) -> GitStatus:
    _ensure_repo()
    message = request.message.strip()
    if not message:
        raise HTTPException(status_code=400, detail="Commit message is required")
    _run_git(["commit", "-m", message])
    return git_status()


def git_push() -> dict[str, str]:
    _ensure_repo()
    result = _run_git(["push"])
    output = (result.stdout + result.stderr).strip()
    return {"status": "ok", "output": output}
