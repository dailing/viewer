import json
import mimetypes
from hashlib import sha256
from pathlib import Path
from urllib.parse import unquote, urlsplit

from fastapi import HTTPException
from loguru import logger

from .config import settings
from .models import ConfigData, DirectoryListing, FileEntry, FileMeta, TextLineWindow, WorkspaceConfig, WorkspaceData, WorkspaceSnapshot
from .storage import AGENT_TASK_LOG_DIR, CONFIG_PATH, migrate_legacy_state
from .users import default_user_id, list_user_profiles, user_home_path, user_workspaces_path

IMAGE_EXTENSIONS = {".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg"}
MARKDOWN_EXTENSIONS = {".md", ".markdown"}
TEXT_EXTENSIONS = {
    ".env",
    ".txt",
    ".py",
    ".js",
    ".ts",
    ".tsx",
    ".jsx",
    ".vue",
    ".css",
    ".scss",
    ".json",
    ".yaml",
    ".yml",
    ".toml",
    ".ini",
    ".cfg",
    ".sh",
    ".bash",
    ".zsh",
    ".c",
    ".cc",
    ".cpp",
    ".h",
    ".hpp",
    ".rs",
    ".go",
    ".java",
    ".kt",
    ".sql",
    ".log",
    ".csv",
}
HTML_EXTENSIONS = {".html", ".htm"}
LINE_INDEX_CHUNK_BYTES = 1024 * 1024
MAX_TEXT_LINE_COUNT = 500
MAX_LINE_INDEX_CACHE_ENTRIES = 8
_line_index_cache: dict[tuple[str, int, int], list[int]] = {}
TEXT_FILENAMES = {".env"}
AGENT_TASK_FILE_PREFIX = "__agent_task_files__"


def normalize_relative(path: str | None) -> str:
    raw = (path or "").replace("\\", "/").strip("/")
    parts = [part for part in raw.split("/") if part and part != "."]
    if any(part == ".." for part in parts):
        raise HTTPException(status_code=400, detail="Parent path segments are not allowed")
    return "/".join(parts)


def served_root(user_id: str | None = None) -> Path:
    return user_home_path(user_id) if user_id else settings.root_resolved


def _resolve_agent_task_file_path(rel: str) -> Path | None:
    parts = rel.split("/")
    if len(parts) < 3 or parts[0] != AGENT_TASK_FILE_PREFIX or parts[2] != "workspace":
        return None
    task_id = parts[1]
    if not task_id.startswith("task_"):
        raise HTTPException(status_code=400, detail="Invalid task file path")
    return AGENT_TASK_LOG_DIR / task_id / "workspace" / "/".join(parts[3:])


def resolve_path(path: str | None, user_id: str | None = None) -> Path:
    raw = (path or "").strip()
    if raw:
        raw_path = Path(raw).expanduser()
        if raw_path.is_absolute():
            return raw_path
    rel = normalize_relative(path)
    task_file = _resolve_agent_task_file_path(rel)
    if task_file is not None:
        return task_file
    return served_root(user_id).joinpath(rel)


def _normalize_link_parts(parts: list[str]) -> str:
    cleaned: list[str] = []
    for part in parts:
        if not part or part == ".":
            continue
        if part == "..":
            if not cleaned:
                raise HTTPException(status_code=400, detail="Markdown link escapes the served root")
            cleaned.pop()
            continue
        cleaned.append(part)
    return "/".join(cleaned)


def _strip_editor_line_suffix(target_path: str) -> str:
    path, separator, suffix = target_path.rpartition(":")
    if not separator or not suffix.isdigit() or not path:
        return target_path
    column_path, column_separator, column_suffix = path.rpartition(":")
    if column_separator and column_suffix.isdigit() and column_path:
        return column_path
    return path


def resolve_markdown_link(base_path: str, target: str, user_id: str | None = None) -> str:
    parsed = urlsplit((target or "").strip())
    scheme = parsed.scheme.lower()
    if scheme and scheme != "file":
        raise HTTPException(status_code=400, detail="Only local file links can be resolved")
    if parsed.netloc and scheme != "file":
        raise HTTPException(status_code=400, detail="Network links cannot be resolved as local files")
    if scheme == "file" and parsed.netloc and parsed.netloc not in {"localhost", "127.0.0.1"}:
        raise HTTPException(status_code=400, detail="Remote file links cannot be resolved as local files")

    target_path = _strip_editor_line_suffix(unquote(parsed.path).replace("\\", "/"))
    if not target_path:
        raise HTTPException(status_code=400, detail="Markdown link target is empty")

    if scheme == "file" or target_path.startswith("/"):
        absolute_target = Path(target_path).expanduser().resolve()
        return relative_for(absolute_target, user_id)

    base_raw = Path((base_path or "").strip()).expanduser()
    if base_raw.is_absolute():
        return relative_for(base_raw.parent.joinpath(target_path).resolve(strict=False), user_id)

    base_rel = normalize_relative(base_path)
    base_parts = base_rel.split("/")[:-1] if base_rel else []
    return _normalize_link_parts([*base_parts, *target_path.split("/")])


def resolve_directory_link(base_dir: str | None, target: str, user_id: str | None = None) -> str:
    parsed = urlsplit((target or "").strip())
    scheme = parsed.scheme.lower()
    if scheme and scheme != "file":
        raise HTTPException(status_code=400, detail="Only local file links can be resolved")
    if parsed.netloc and scheme != "file":
        raise HTTPException(status_code=400, detail="Network links cannot be resolved as local files")
    if scheme == "file" and parsed.netloc and parsed.netloc not in {"localhost", "127.0.0.1"}:
        raise HTTPException(status_code=400, detail="Remote file links cannot be resolved as local files")

    target_path = _strip_editor_line_suffix(unquote(parsed.path).replace("\\", "/"))
    if not target_path:
        raise HTTPException(status_code=400, detail="Link target is empty")

    if scheme == "file" or target_path.startswith("/"):
        absolute_target = Path(target_path).expanduser().resolve()
        return relative_for(absolute_target, user_id)

    base_raw = Path((base_dir or "").strip()).expanduser()
    if base_raw.is_absolute():
        return relative_for(base_raw.joinpath(target_path).resolve(strict=False), user_id)

    base_rel = normalize_relative(base_dir or "")
    base_parts = base_rel.split("/") if base_rel else []
    return _normalize_link_parts([*base_parts, *target_path.split("/")])


def resolve_served_directory(path: str | None, label: str, user_id: str | None = None) -> str:
    raw = (path or "").strip()
    requested = normalize_relative(raw)
    if raw:
        raw_path = Path(raw).expanduser()
        if raw_path.is_absolute():
            try:
                target = raw_path.resolve()
                requested = target.as_posix()
            except OSError:
                logger.warning("{} cwd '{}' is not available; using root", label, raw)
                return served_root(user_id).as_posix()
        else:
            target = resolve_path(requested, user_id)
    elif user_id:
        target = user_home_path(user_id)
        requested = target.as_posix()
    else:
        target = settings.root_resolved
    if not target.exists() or not target.is_dir():
        if requested:
            logger.warning("{} cwd '{}' is not available; using root", label, requested)
        return served_root(user_id).as_posix()
    return target.as_posix()


def relative_for(path: Path, user_id: str | None = None) -> str:
    try:
        rel = path.relative_to(served_root(user_id)).as_posix()
        return "" if rel == "." else rel
    except ValueError:
        return path.as_posix()


def guess_mime(path: Path) -> str:
    return mimetypes.guess_type(path.name)[0] or "application/octet-stream"


def preview_kind(path: Path, mime: str, size: int) -> str:
    suffix = path.suffix.lower()
    filename = path.name.lower()
    if suffix in IMAGE_EXTENSIONS:
        return "image"
    if suffix in MARKDOWN_EXTENSIONS:
        return "markdown"
    if suffix in HTML_EXTENSIONS or mime == "text/html":
        return "html"
    if suffix == ".pdf" or mime == "application/pdf":
        return "pdf"
    if filename in TEXT_FILENAMES or filename.startswith(".env.") or suffix in TEXT_EXTENSIONS or mime.startswith("text/"):
        return "text"
    return "unsupported"


def content_hash(path: Path) -> str:
    digest = sha256()
    with path.open("rb") as handle:
        while True:
            chunk = handle.read(1024 * 1024)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def metadata_version(path: Path, stat) -> str:
    return f"{stat.st_mtime_ns:x}-{stat.st_size:x}"


def entry_for(path: Path, user_id: str | None = None) -> FileEntry:
    try:
        stat = path.stat()
    except OSError:
        stat = None

    is_symlink = path.is_symlink()
    is_dir = path.is_dir()
    if is_dir:
        entry_type = "symlink" if is_symlink else "directory"
    elif path.is_file():
        entry_type = "symlink" if is_symlink else "file"
    else:
        entry_type = "other"

    return FileEntry(
        name=path.name,
        path=relative_for(path, user_id),
        type=entry_type,
        size=stat.st_size if stat and not is_dir else None,
        mtime=stat.st_mtime if stat else None,
        mime=guess_mime(path) if not is_dir else None,
        is_dir=is_dir,
        is_symlink=is_symlink,
    )


def list_directory(path: str | None, user_id: str | None = None) -> DirectoryListing:
    target = resolve_path(path, user_id)
    if not target.exists():
        raise HTTPException(status_code=404, detail="Directory not found")
    if not target.is_dir():
        raise HTTPException(status_code=400, detail="Path is not a directory")

    entries = []
    for child in target.iterdir():
        if not settings.show_hidden and child.name.startswith("."):
            continue
        entries.append(entry_for(child, user_id))

    entries.sort(key=lambda item: (not item.is_dir, item.name.lower()))
    return DirectoryListing(path=relative_for(target, user_id), entries=entries)


def upload_target(directory: str | None, filename: str, user_id: str | None = None) -> Path:
    if not filename or "/" in filename or "\\" in filename or filename in {".", ".."}:
        raise HTTPException(status_code=400, detail="Invalid file name")
    target_dir = resolve_path(directory, user_id)
    if not target_dir.exists():
        raise HTTPException(status_code=404, detail="Upload directory not found")
    if not target_dir.is_dir():
        raise HTTPException(status_code=400, detail="Upload target is not a directory")
    target = target_dir / filename
    if target.exists() and target.is_dir():
        raise HTTPException(status_code=400, detail="Cannot overwrite a directory")
    return target


def delete_file(path: str, user_id: str | None = None) -> dict[str, str]:
    target = resolve_path(path, user_id)
    if not target.exists():
        raise HTTPException(status_code=404, detail="File not found")
    if target.is_dir():
        raise HTTPException(status_code=400, detail="Directory deletion is not supported")
    target.unlink()
    return {"status": "deleted", "path": relative_for(target, user_id)}


def get_meta(path: str, user_id: str | None = None) -> FileMeta:
    target = resolve_path(path, user_id)
    if not target.exists():
        raise HTTPException(status_code=404, detail="File not found")
    if not target.is_file():
        raise HTTPException(status_code=400, detail="Path is not a file")

    stat = target.stat()
    mime = guess_mime(target)
    preview = preview_kind(target, mime, stat.st_size)
    text_too_large = preview in {"text", "markdown", "html"} and stat.st_size > settings.max_text_preview_bytes
    return FileMeta(
        name=target.name,
        path=relative_for(target, user_id),
        size=stat.st_size,
        mtime=stat.st_mtime,
        content_hash=metadata_version(target, stat) if text_too_large else content_hash(target),
        mime=mime,
        preview=preview,
        text_too_large=text_too_large,
    )


def read_text(path: str, user_id: str | None = None) -> str:
    meta = get_meta(path, user_id)
    if meta.text_too_large:
        raise HTTPException(status_code=413, detail="Text preview is too large")
    target = resolve_path(path, user_id)
    try:
        return target.read_text(encoding="utf-8")
    except UnicodeDecodeError:
        return target.read_text(encoding="utf-8", errors="replace")


def _line_offsets(path: Path, stat) -> list[int]:
    cache_key = (path.as_posix(), stat.st_mtime_ns, stat.st_size)
    cached = _line_index_cache.get(cache_key)
    if cached is not None:
        return cached

    offsets = [0]
    position = 0
    with path.open("rb") as handle:
        while True:
            chunk = handle.read(LINE_INDEX_CHUNK_BYTES)
            if not chunk:
                break
            start = 0
            while True:
                newline_at = chunk.find(b"\n", start)
                if newline_at < 0:
                    break
                offsets.append(position + newline_at + 1)
                start = newline_at + 1
            position += len(chunk)

    stale_keys = [key for key in _line_index_cache if key[0] == path.as_posix() and key != cache_key]
    for key in stale_keys:
        _line_index_cache.pop(key, None)
    _line_index_cache[cache_key] = offsets
    while len(_line_index_cache) > MAX_LINE_INDEX_CACHE_ENTRIES:
        oldest_key = next(iter(_line_index_cache))
        _line_index_cache.pop(oldest_key, None)
    return offsets


def read_text_lines(path: str, start_line: int = 0, count: int = 200, user_id: str | None = None) -> TextLineWindow:
    meta = get_meta(path, user_id)
    if meta.preview not in {"text", "markdown", "html"}:
        raise HTTPException(status_code=400, detail="Path is not a text preview")
    target = resolve_path(path, user_id)
    stat = target.stat()
    offsets = _line_offsets(target, stat)
    total_lines = len(offsets)
    safe_start = max(0, min(start_line, max(total_lines - 1, 0)))
    safe_count = max(1, min(count, MAX_TEXT_LINE_COUNT))
    end_line = min(safe_start + safe_count, total_lines)
    start_offset = offsets[safe_start] if offsets else 0
    end_offset = offsets[end_line] if end_line < total_lines else stat.st_size

    with target.open("rb") as handle:
        handle.seek(start_offset)
        raw = handle.read(max(0, end_offset - start_offset))
    text = raw.decode("utf-8", errors="replace")
    lines = text.splitlines()
    if text.endswith(("\n", "\r")) and len(lines) < end_line - safe_start:
        lines.append("")

    return TextLineWindow(
        path=relative_for(target, user_id),
        size=stat.st_size,
        mtime=stat.st_mtime,
        total_lines=total_lines,
        start_line=safe_start,
        lines=lines[: safe_count],
        truncated_start=safe_start > 0,
        truncated_end=end_line < total_lines,
    )


def write_text(path: str, content: str, user_id: str | None = None) -> FileMeta:
    target = resolve_path(path, user_id)
    if not target.exists():
        raise HTTPException(status_code=404, detail="File not found")
    if not target.is_file():
        raise HTTPException(status_code=400, detail="Path is not a file")
    target.write_text(content, encoding="utf-8")
    return get_meta(path, user_id)


def config_path() -> Path:
    migrate_legacy_state()
    return CONFIG_PATH


def read_config() -> ConfigData:
    path = config_path()
    if not path.exists():
        config = ConfigData(
            workspace=WorkspaceConfig(count=legacy_workspace_count() or 5),
            users=list_user_profiles(),
            default_user=default_user_id(),
        )
        write_config(config)
        return config
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
        config = ConfigData.model_validate(raw)
    except Exception:
        return ConfigData(users=list_user_profiles(), default_user=default_user_id())
    if isinstance(raw, dict) and "workspace" not in raw:
        config.workspace.count = legacy_workspace_count() or config.workspace.count
    cleaned = ConfigData(
        appearance=config.appearance,
        markdown=config.markdown,
        codex=config.codex,
        voice=config.voice,
        dag=config.dag,
        workspace=config.workspace,
        users=list_user_profiles(),
        default_user=default_user_id(),
    )
    codex_raw = raw.get("codex") if isinstance(raw, dict) else None
    voice_raw = raw.get("voice") if isinstance(raw, dict) else None
    dag_raw = raw.get("dag") if isinstance(raw, dict) else None
    workspace_raw = raw.get("workspace") if isinstance(raw, dict) else None
    missing_codex_defaults = not isinstance(codex_raw, dict) or "auto_commit_prompt" not in codex_raw
    missing_voice_defaults = (
        not isinstance(voice_raw, dict)
        or "available_models" not in voice_raw
        or "available_languages" not in voice_raw
        or "translation_enabled" not in voice_raw
    )
    missing_dag_defaults = not isinstance(dag_raw, dict) or "base_url" not in dag_raw
    missing_workspace_defaults = not isinstance(workspace_raw, dict) or "heat_interval_seconds" not in workspace_raw or "heat_step_percent" not in workspace_raw
    missing_user_defaults = "users" not in raw or "default_user" not in raw
    if isinstance(raw, dict) and (
        missing_codex_defaults
        or missing_voice_defaults
        or missing_dag_defaults
        or missing_workspace_defaults
        or missing_user_defaults
        or "workspace" not in raw
        or any(key in raw for key in ("pinned", "current_path", "visit_times", "workspaces"))
    ):
        write_config(cleaned)
    return cleaned


def write_config(config: ConfigData) -> ConfigData:
    path = config_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(config.model_dump_json(indent=2), encoding="utf-8")
    return config


def write_workspace_config(config: WorkspaceConfig, user_id: str | None = None) -> WorkspaceData:
    current = read_config()
    current.workspace = config
    write_config(current)
    data = read_workspaces(user_id)
    data.count = config.count
    return data


def workspaces_path(user_id: str | None = None) -> Path:
    migrate_legacy_state()
    return user_workspaces_path(user_id)


def read_workspaces(user_id: str | None = None) -> WorkspaceData:
    path = workspaces_path(user_id)
    if not path.exists():
        data = write_workspaces(legacy_workspace_data(), user_id)
        data.count = configured_workspace_count()
        return data
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
        migrated = migrate_workspace_state(raw)
        data = WorkspaceData.model_validate(migrated)
    except Exception:
        logger.warning("Ignoring invalid workspace state file {}", path)
        data = WorkspaceData()
        data.count = configured_workspace_count()
        return data
    if isinstance(raw, dict) and "count" in raw:
        ensure_workspace_config_count(data.count)
    if migrated is not raw or (isinstance(raw, dict) and "count" in raw):
        write_workspaces(data, user_id)
    data.count = configured_workspace_count()
    return data


def write_workspaces(data: WorkspaceData, user_id: str | None = None) -> WorkspaceData:
    path = workspaces_path(user_id)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(data.model_dump_json(indent=2, exclude={"count"}), encoding="utf-8")
    return data


def _unique_nonempty_strings(items: list[str]) -> list[str]:
    return list(dict.fromkeys(item.strip() for item in items if item.strip()))


def _layout_session_ids(layout: object, key: str) -> list[str]:
    ids: list[str] = []

    def visit(node: object) -> None:
        if not isinstance(node, dict):
            return
        if node.get("type") == "pane":
            value = node.get(key)
            if isinstance(value, str) and value.strip():
                ids.append(value.strip())
            return
        visit(node.get("first"))
        visit(node.get("second"))

    visit(layout)
    return _unique_nonempty_strings(ids)


def _layout_agent_refs(layout: object) -> list[str]:
    refs = _layout_session_ids(layout, "agentSession")
    refs.extend(f"codex:{item}" for item in _layout_session_ids(layout, "codexSessionId"))
    refs.extend(f"hermes:{item}" for item in _layout_session_ids(layout, "hermesSessionId"))
    return _unique_nonempty_strings(refs)


def write_workspace(workspace_id: str, snapshot: WorkspaceSnapshot, user_id: str | None = None) -> WorkspaceData:
    cleaned_id = workspace_id.strip()
    if not cleaned_id:
        raise HTTPException(status_code=400, detail="Workspace id is required")
    current_path = normalize_relative(snapshot.current_path)
    pinned = []
    seen = set()
    for item in snapshot.pinned or []:
        cleaned = normalize_relative(item)
        if cleaned not in seen:
            pinned.append(cleaned)
            seen.add(cleaned)
    visit_times = {}
    for path, visited_at in snapshot.visit_times.items():
        cleaned = normalize_relative(path)
        visit_times[cleaned] = visited_at
    data = read_workspaces(user_id)
    previous = data.slots.get(cleaned_id)
    agent_refs = list(previous.agent_session_ids if previous else [])
    pinned_agent_refs = list(previous.pinned_agent_session_ids if previous else snapshot.pinned_agent_session_ids)
    data.active_workspace_id = cleaned_id
    data.slots[cleaned_id] = WorkspaceSnapshot(
        layout=snapshot.layout,
        active_pane_id=snapshot.active_pane_id,
        current_path=current_path,
        pinned=pinned,
        agent_session_ids=_unique_nonempty_strings(agent_refs),
        pinned_agent_session_ids=_unique_nonempty_strings(pinned_agent_refs),
        visit_times=visit_times,
        updated_at=snapshot.updated_at,
    )
    return write_workspaces(data, user_id)


def add_workspace_agent_session(workspace_id: str, ref: str, user_id: str | None = None) -> WorkspaceData:
    cleaned_id = workspace_id.strip()
    cleaned_ref = ref.strip()
    if not cleaned_id:
        raise HTTPException(status_code=400, detail="Workspace id is required")
    if not cleaned_ref or ":" not in cleaned_ref:
        raise HTTPException(status_code=400, detail="Agent session ref is required")
    provider, session_id = cleaned_ref.split(":", 1)
    if not provider or not session_id:
        raise HTTPException(status_code=400, detail="Agent session ref is invalid")
    data = read_workspaces(user_id)
    snapshot = data.slots.get(cleaned_id) or WorkspaceSnapshot(layout={"type": "pane", "id": f"pane-workspace-{cleaned_id}"})
    agent_refs = _unique_nonempty_strings([*snapshot.agent_session_ids, cleaned_ref])
    data.slots[cleaned_id] = snapshot.model_copy(
        update={
            "agent_session_ids": agent_refs,
        }
    )
    return write_workspaces(data, user_id)


def remove_workspace_agent_session(workspace_id: str, ref: str, user_id: str | None = None) -> WorkspaceData:
    cleaned_id = workspace_id.strip()
    cleaned_ref = ref.strip()
    if not cleaned_id:
        raise HTTPException(status_code=400, detail="Workspace id is required")
    if not cleaned_ref or ":" not in cleaned_ref:
        raise HTTPException(status_code=400, detail="Agent session ref is required")
    data = read_workspaces(user_id)
    snapshot = data.slots.get(cleaned_id)
    if not snapshot:
        return data
    data.slots[cleaned_id] = snapshot.model_copy(
        update={
            "agent_session_ids": [item for item in snapshot.agent_session_ids if item != cleaned_ref],
            "pinned_agent_session_ids": [item for item in snapshot.pinned_agent_session_ids if item != cleaned_ref],
        }
    )
    return write_workspaces(data, user_id)


def add_workspace_pinned_agent_session(workspace_id: str, ref: str, user_id: str | None = None) -> WorkspaceData:
    cleaned_id = workspace_id.strip()
    cleaned_ref = ref.strip()
    if not cleaned_id:
        raise HTTPException(status_code=400, detail="Workspace id is required")
    if not cleaned_ref or ":" not in cleaned_ref:
        raise HTTPException(status_code=400, detail="Agent session ref is required")
    provider, session_id = cleaned_ref.split(":", 1)
    if not provider or not session_id:
        raise HTTPException(status_code=400, detail="Agent session ref is invalid")
    data = read_workspaces(user_id)
    snapshot = data.slots.get(cleaned_id) or WorkspaceSnapshot(layout={"type": "pane", "id": f"pane-workspace-{cleaned_id}"})
    data.slots[cleaned_id] = snapshot.model_copy(
        update={
            "agent_session_ids": _unique_nonempty_strings([*snapshot.agent_session_ids, cleaned_ref]),
            "pinned_agent_session_ids": _unique_nonempty_strings([*snapshot.pinned_agent_session_ids, cleaned_ref]),
        }
    )
    return write_workspaces(data, user_id)


def remove_workspace_pinned_agent_session(workspace_id: str, ref: str, user_id: str | None = None) -> WorkspaceData:
    cleaned_id = workspace_id.strip()
    cleaned_ref = ref.strip()
    if not cleaned_id:
        raise HTTPException(status_code=400, detail="Workspace id is required")
    if not cleaned_ref or ":" not in cleaned_ref:
        raise HTTPException(status_code=400, detail="Agent session ref is required")
    data = read_workspaces(user_id)
    snapshot = data.slots.get(cleaned_id)
    if not snapshot:
        return data
    data.slots[cleaned_id] = snapshot.model_copy(
        update={"pinned_agent_session_ids": [item for item in snapshot.pinned_agent_session_ids if item != cleaned_ref]}
    )
    return write_workspaces(data, user_id)


def set_active_workspace(workspace_id: str, user_id: str | None = None) -> WorkspaceData:
    cleaned_id = workspace_id.strip()
    if not cleaned_id:
        raise HTTPException(status_code=400, detail="Workspace id is required")
    data = read_workspaces(user_id)
    data.active_workspace_id = cleaned_id
    return write_workspaces(data, user_id)


def legacy_workspace_count() -> int | None:
    if not CONFIG_PATH.exists():
        return None
    try:
        raw = json.loads(CONFIG_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    value = raw.get("workspaces") if isinstance(raw, dict) else None
    count = value.get("count") if isinstance(value, dict) else None
    if not isinstance(count, (int, float)):
        return None
    return max(1, min(20, round(count)))


def ensure_workspace_config_count(count: int) -> None:
    config_path()
    try:
        raw = json.loads(CONFIG_PATH.read_text(encoding="utf-8")) if CONFIG_PATH.exists() else {}
    except (OSError, json.JSONDecodeError):
        raw = {}
    if isinstance(raw, dict) and isinstance(raw.get("workspace"), dict):
        return
    try:
        config = ConfigData.model_validate(raw if isinstance(raw, dict) else {})
    except Exception:
        config = ConfigData()
    config.workspace.count = max(1, min(20, round(count)))
    write_config(config)


def configured_workspace_count() -> int:
    if CONFIG_PATH.exists():
        try:
            raw = json.loads(CONFIG_PATH.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            raw = None
        if isinstance(raw, dict):
            workspace = raw.get("workspace")
            count = workspace.get("count") if isinstance(workspace, dict) else None
            if isinstance(count, (int, float)):
                return max(1, min(20, round(count)))
    return legacy_workspace_count() or 5


def legacy_visit_times() -> dict[str, float]:
    if not CONFIG_PATH.exists():
        return {}
    try:
        raw = json.loads(CONFIG_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    value = raw.get("visit_times") if isinstance(raw, dict) else None
    if not isinstance(value, dict):
        return {}
    visit_times = {}
    for path, visited_at in value.items():
        if isinstance(visited_at, (int, float)):
            visit_times[normalize_relative(path)] = float(visited_at)
    return visit_times


def legacy_workspace_data() -> WorkspaceData:
    data = WorkspaceData(count=configured_workspace_count())
    if not CONFIG_PATH.exists():
        return data
    try:
        raw = json.loads(CONFIG_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return data
    if not isinstance(raw, dict):
        return data

    pinned = []
    seen = set()
    for item in raw.get("pinned") or []:
        if not isinstance(item, str):
            continue
        try:
            cleaned = normalize_relative(item)
        except HTTPException:
            continue
        if cleaned and cleaned not in seen:
            pinned.append(cleaned)
            seen.add(cleaned)
    try:
        current_path = normalize_relative(raw.get("current_path") if isinstance(raw.get("current_path"), str) else "")
    except HTTPException:
        current_path = ""
    visit_times = legacy_visit_times()
    if current_path or pinned or visit_times:
        data.slots[data.active_workspace_id] = WorkspaceSnapshot(
            current_path=current_path,
            pinned=pinned,
            visit_times=visit_times,
        )
    return data


def migrate_workspace_state(raw: object) -> object:
    if not isinstance(raw, dict):
        return raw
    slots = raw.get("slots")
    if not isinstance(slots, dict):
        slots = {}
    changed = False
    migrated = dict(raw)
    migrated_slots = dict(slots)
    if "count" not in migrated:
        migrated["count"] = configured_workspace_count()
        changed = True
    active_workspace_id = str(migrated.get("active_workspace_id") or "1")
    active_visit_times = legacy_visit_times()
    for workspace_id, snapshot in slots.items():
        if not isinstance(snapshot, dict):
            continue
        next_snapshot = dict(snapshot)
        refs = [item for item in next_snapshot.get("agent_session_ids", []) if isinstance(item, str)]
        refs.extend(f"codex:{item}" for item in next_snapshot.get("codex_session_ids", []) if isinstance(item, str))
        refs.extend(f"hermes:{item}" for item in next_snapshot.get("hermes_session_ids", []) if isinstance(item, str))
        refs.extend(_layout_agent_refs(next_snapshot.get("layout")))
        agent_refs = _unique_nonempty_strings(refs)
        if next_snapshot.get("agent_session_ids") != agent_refs:
            next_snapshot["agent_session_ids"] = agent_refs
            changed = True
        pinned_agent_refs = _unique_nonempty_strings([item for item in next_snapshot.get("pinned_agent_session_ids", []) if isinstance(item, str)])
        if next_snapshot.get("pinned_agent_session_ids") != pinned_agent_refs:
            next_snapshot["pinned_agent_session_ids"] = pinned_agent_refs
            changed = True
        if "visit_times" not in next_snapshot:
            next_snapshot["visit_times"] = active_visit_times if str(workspace_id) == active_workspace_id else {}
            changed = True
        migrated_slots[workspace_id] = next_snapshot
    if not changed:
        return raw
    migrated["slots"] = migrated_slots
    return migrated
