import json
import mimetypes
from hashlib import sha256
from pathlib import Path
from urllib.parse import unquote, urlsplit

from fastapi import HTTPException
from loguru import logger

from .config import settings
from .models import ConfigData, DirectoryListing, FileEntry, FileMeta, TextLineWindow
from .storage import CONFIG_PATH
from .users import default_user_id, list_user_profiles, user_home_path

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

def normalize_relative(path: str | None) -> str:
    raw = (path or "").replace("\\", "/").strip("/")
    parts = [part for part in raw.split("/") if part and part != "."]
    if any(part == ".." for part in parts):
        raise HTTPException(status_code=400, detail="Parent path segments are not allowed")
    return "/".join(parts)


def served_root(user_id: str | None = None) -> Path:
    return user_home_path(user_id) if user_id else settings.root_resolved


def resolve_path(path: str | None, user_id: str | None = None) -> Path:
    raw = (path or "").strip()
    if raw:
        raw_path = Path(raw).expanduser()
        if raw_path.is_absolute():
            return raw_path
    rel = normalize_relative(path)
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
    return CONFIG_PATH


def read_config() -> ConfigData:
    path = config_path()
    if not path.exists():
        config = ConfigData(users=list_user_profiles(), default_user=default_user_id())
        write_config(config)
        return config
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
        config = ConfigData.model_validate(raw)
    except Exception:
        return ConfigData(users=list_user_profiles(), default_user=default_user_id())
    cleaned = ConfigData(
        appearance=config.appearance,
        markdown=config.markdown,
        codex=config.codex,
        voice=config.voice,
        super_workspace=config.super_workspace,
        users=list_user_profiles(),
        default_user=default_user_id(),
    )
    codex_raw = raw.get("codex") if isinstance(raw, dict) else None
    voice_raw = raw.get("voice") if isinstance(raw, dict) else None
    super_workspace_raw = raw.get("super_workspace") if isinstance(raw, dict) else None
    missing_codex_defaults = not isinstance(codex_raw, dict)
    missing_voice_defaults = (
        not isinstance(voice_raw, dict)
        or "available_models" not in voice_raw
        or "available_languages" not in voice_raw
        or "translation_enabled" not in voice_raw
    )
    missing_super_workspace_defaults = (
        not isinstance(super_workspace_raw, dict)
        or "chat_history_bootstrap_tokens" not in super_workspace_raw
        or "dispatch_profiles" not in super_workspace_raw
        or "active_dispatch_profile_id" not in super_workspace_raw
        or "dispatch_history_word_budget" not in super_workspace_raw
        or "dispatch_prompt_template" not in super_workspace_raw
    )
    missing_user_defaults = "users" not in raw or "default_user" not in raw
    if isinstance(raw, dict) and (
        missing_codex_defaults
        or missing_voice_defaults
        or missing_super_workspace_defaults
        or missing_user_defaults
    ):
        write_config(cleaned)
    return cleaned


def write_config(config: ConfigData) -> ConfigData:
    path = config_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(config.model_dump_json(indent=2), encoding="utf-8")
    return config
