import json
import mimetypes
from hashlib import sha256
from pathlib import Path
from urllib.parse import unquote, urlsplit

from fastapi import HTTPException
from loguru import logger

from .config import settings
from .models import ConfigData, DirectoryListing, FileEntry, FileMeta, WorkspaceConfig, WorkspaceData, WorkspaceSnapshot
from .storage import CONFIG_PATH, WORKSPACES_PATH, migrate_legacy_state

IMAGE_EXTENSIONS = {".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg"}
MARKDOWN_EXTENSIONS = {".md", ".markdown"}
TEXT_EXTENSIONS = {
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


def normalize_relative(path: str | None) -> str:
    raw = (path or "").replace("\\", "/").strip("/")
    parts = [part for part in raw.split("/") if part and part != "."]
    if any(part == ".." for part in parts):
        raise HTTPException(status_code=400, detail="Parent path segments are not allowed")
    return "/".join(parts)


def resolve_path(path: str | None) -> Path:
    rel = normalize_relative(path)
    return settings.root_resolved.joinpath(rel)


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


def resolve_markdown_link(base_path: str, target: str) -> str:
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
        try:
            return absolute_target.relative_to(settings.root_resolved.resolve()).as_posix()
        except ValueError as exc:
            raise HTTPException(status_code=400, detail="Markdown link target is outside the served root") from exc

    base_rel = normalize_relative(base_path)
    base_parts = base_rel.split("/")[:-1] if base_rel else []
    return _normalize_link_parts([*base_parts, *target_path.split("/")])


def resolve_directory_link(base_dir: str | None, target: str) -> str:
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
        try:
            return absolute_target.relative_to(settings.root_resolved.resolve()).as_posix()
        except ValueError as exc:
            raise HTTPException(status_code=400, detail="Link target is outside the served root") from exc

    base_rel = normalize_relative(base_dir or "")
    base_parts = base_rel.split("/") if base_rel else []
    return _normalize_link_parts([*base_parts, *target_path.split("/")])


def resolve_served_directory(path: str | None, label: str) -> str:
    raw = (path or "").strip()
    requested = normalize_relative(raw)
    if raw:
        raw_path = Path(raw).expanduser()
        if raw_path.is_absolute():
            try:
                target = raw_path.resolve()
                requested = target.relative_to(settings.root_resolved).as_posix()
            except (OSError, ValueError):
                logger.warning("{} cwd '{}' is outside served root; using root", label, raw)
                return settings.root_resolved.as_posix()
        else:
            target = resolve_path(requested)
    else:
        target = settings.root_resolved
    if not target.exists() or not target.is_dir():
        if requested:
            logger.warning("{} cwd '{}' is not available; using root", label, requested)
        return settings.root_resolved.as_posix()
    return target.as_posix()


def relative_for(path: Path) -> str:
    try:
        return path.relative_to(settings.root_resolved).as_posix()
    except ValueError:
        return path.as_posix()


def guess_mime(path: Path) -> str:
    return mimetypes.guess_type(path.name)[0] or "application/octet-stream"


def preview_kind(path: Path, mime: str, size: int) -> str:
    suffix = path.suffix.lower()
    if suffix in IMAGE_EXTENSIONS:
        return "image"
    if suffix in MARKDOWN_EXTENSIONS:
        return "markdown"
    if suffix in HTML_EXTENSIONS or mime == "text/html":
        return "html"
    if suffix == ".pdf" or mime == "application/pdf":
        return "pdf"
    if suffix in TEXT_EXTENSIONS or mime.startswith("text/"):
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


def entry_for(path: Path) -> FileEntry:
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
        path=relative_for(path),
        type=entry_type,
        size=stat.st_size if stat and not is_dir else None,
        mtime=stat.st_mtime if stat else None,
        mime=guess_mime(path) if not is_dir else None,
        is_dir=is_dir,
        is_symlink=is_symlink,
    )


def list_directory(path: str | None) -> DirectoryListing:
    target = resolve_path(path)
    if not target.exists():
        raise HTTPException(status_code=404, detail="Directory not found")
    if not target.is_dir():
        raise HTTPException(status_code=400, detail="Path is not a directory")

    entries = []
    for child in target.iterdir():
        if not settings.show_hidden and child.name.startswith("."):
            continue
        entries.append(entry_for(child))

    entries.sort(key=lambda item: (not item.is_dir, item.name.lower()))
    return DirectoryListing(path=normalize_relative(path), entries=entries)


def get_meta(path: str) -> FileMeta:
    target = resolve_path(path)
    if not target.exists():
        raise HTTPException(status_code=404, detail="File not found")
    if not target.is_file():
        raise HTTPException(status_code=400, detail="Path is not a file")

    stat = target.stat()
    mime = guess_mime(target)
    preview = preview_kind(target, mime, stat.st_size)
    return FileMeta(
        name=target.name,
        path=normalize_relative(path),
        size=stat.st_size,
        mtime=stat.st_mtime,
        content_hash=content_hash(target),
        mime=mime,
        preview=preview,
        text_too_large=preview in {"text", "markdown", "html"} and stat.st_size > settings.max_text_preview_bytes,
    )


def read_text(path: str) -> str:
    meta = get_meta(path)
    if meta.text_too_large:
        raise HTTPException(status_code=413, detail="Text preview is too large")
    target = resolve_path(path)
    try:
        return target.read_text(encoding="utf-8")
    except UnicodeDecodeError:
        return target.read_text(encoding="utf-8", errors="replace")


def config_path() -> Path:
    migrate_legacy_state()
    return CONFIG_PATH


def read_config() -> ConfigData:
    path = config_path()
    if not path.exists():
        config = ConfigData(workspace=WorkspaceConfig(count=legacy_workspace_count() or stored_workspace_count() or 5))
        write_config(config)
        return config
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
        config = ConfigData.model_validate(raw)
    except Exception:
        return ConfigData()
    if isinstance(raw, dict) and "workspace" not in raw:
        config.workspace.count = legacy_workspace_count() or stored_workspace_count() or config.workspace.count
    cleaned = ConfigData(
        appearance=config.appearance,
        markdown=config.markdown,
        codex=config.codex,
        workspace=config.workspace,
    )
    codex_raw = raw.get("codex") if isinstance(raw, dict) else None
    workspace_raw = raw.get("workspace") if isinstance(raw, dict) else None
    missing_codex_defaults = not isinstance(codex_raw, dict) or "auto_commit_prompt" not in codex_raw
    missing_workspace_defaults = not isinstance(workspace_raw, dict) or "heat_interval_seconds" not in workspace_raw or "heat_step_percent" not in workspace_raw
    if isinstance(raw, dict) and (
        missing_codex_defaults
        or missing_workspace_defaults
        or "workspace" not in raw
        or any(key in raw for key in ("pinned", "current_path", "visit_times", "workspaces"))
    ):
        read_workspaces()
        write_config(cleaned)
    return cleaned


def write_config(config: ConfigData) -> ConfigData:
    path = config_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(config.model_dump_json(indent=2), encoding="utf-8")
    return config


def write_workspace_config(config: WorkspaceConfig) -> WorkspaceData:
    current = read_config()
    current.workspace = config
    write_config(current)
    data = read_workspaces()
    data.count = config.count
    return data


def workspaces_path() -> Path:
    migrate_legacy_state()
    return WORKSPACES_PATH


def read_workspaces() -> WorkspaceData:
    path = workspaces_path()
    if not path.exists():
        data = write_workspaces(legacy_workspace_data())
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
        write_workspaces(data)
    data.count = configured_workspace_count()
    return data


def write_workspaces(data: WorkspaceData) -> WorkspaceData:
    path = workspaces_path()
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


def write_workspace(workspace_id: str, snapshot: WorkspaceSnapshot) -> WorkspaceData:
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
    agent_refs = list(snapshot.agent_session_ids)
    agent_refs.extend(f"codex:{item}" for item in snapshot.codex_session_ids)
    agent_refs.extend(f"hermes:{item}" for item in snapshot.hermes_session_ids)
    agent_refs.extend(_layout_agent_refs(snapshot.layout))
    data = read_workspaces()
    data.active_workspace_id = cleaned_id
    data.slots[cleaned_id] = WorkspaceSnapshot(
        layout=snapshot.layout,
        active_pane_id=snapshot.active_pane_id,
        current_path=current_path,
        pinned=pinned,
        agent_session_ids=_unique_nonempty_strings(agent_refs),
        codex_session_ids=_unique_nonempty_strings(snapshot.codex_session_ids),
        hermes_session_ids=_unique_nonempty_strings(snapshot.hermes_session_ids),
        visit_times=visit_times,
        updated_at=snapshot.updated_at,
    )
    return write_workspaces(data)


def set_active_workspace(workspace_id: str) -> WorkspaceData:
    cleaned_id = workspace_id.strip()
    if not cleaned_id:
        raise HTTPException(status_code=400, detail="Workspace id is required")
    data = read_workspaces()
    data.active_workspace_id = cleaned_id
    return write_workspaces(data)


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


def stored_workspace_count() -> int | None:
    if not WORKSPACES_PATH.exists():
        return None
    try:
        raw = json.loads(WORKSPACES_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    count = raw.get("count") if isinstance(raw, dict) else None
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
    return legacy_workspace_count() or stored_workspace_count() or 5


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
        if "agent_session_ids" not in next_snapshot:
            refs = _layout_agent_refs(next_snapshot.get("layout"))
            refs.extend(f"codex:{item}" for item in next_snapshot.get("codex_session_ids", []) if isinstance(item, str))
            refs.extend(f"hermes:{item}" for item in next_snapshot.get("hermes_session_ids", []) if isinstance(item, str))
            next_snapshot["agent_session_ids"] = _unique_nonempty_strings(refs)
            changed = True
        elif not next_snapshot.get("agent_session_ids"):
            layout_agent_refs = _layout_agent_refs(next_snapshot.get("layout"))
            if layout_agent_refs:
                next_snapshot["agent_session_ids"] = layout_agent_refs
                changed = True
        if "codex_session_ids" not in next_snapshot:
            next_snapshot["codex_session_ids"] = _layout_session_ids(next_snapshot.get("layout"), "codexSessionId")
            changed = True
        elif not next_snapshot.get("codex_session_ids"):
            layout_codex_ids = _layout_session_ids(next_snapshot.get("layout"), "codexSessionId")
            if layout_codex_ids:
                next_snapshot["codex_session_ids"] = layout_codex_ids
                changed = True
        if "hermes_session_ids" not in next_snapshot:
            next_snapshot["hermes_session_ids"] = _layout_session_ids(next_snapshot.get("layout"), "hermesSessionId")
            changed = True
        elif not next_snapshot.get("hermes_session_ids"):
            layout_hermes_ids = _layout_session_ids(next_snapshot.get("layout"), "hermesSessionId")
            if layout_hermes_ids:
                next_snapshot["hermes_session_ids"] = layout_hermes_ids
                changed = True
        if "visit_times" not in next_snapshot:
            next_snapshot["visit_times"] = active_visit_times if str(workspace_id) == active_workspace_id else {}
            changed = True
        migrated_slots[workspace_id] = next_snapshot
    if not changed:
        return raw
    migrated["slots"] = migrated_slots
    return migrated
