import mimetypes
from hashlib import sha256
from pathlib import Path

from fastapi import HTTPException
from loguru import logger

from .config import settings
from .models import ConfigData, DirectoryListing, FileEntry, FileMeta

CONFIG_NAME = ".viewer.config.json"

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
    ".html",
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


def normalize_relative(path: str | None) -> str:
    raw = (path or "").replace("\\", "/").strip("/")
    parts = [part for part in raw.split("/") if part and part != "."]
    if any(part == ".." for part in parts):
        raise HTTPException(status_code=400, detail="Parent path segments are not allowed")
    return "/".join(parts)


def resolve_path(path: str | None) -> Path:
    rel = normalize_relative(path)
    return settings.root_resolved.joinpath(rel)


def resolve_served_directory(path: str | None, label: str) -> str:
    requested = normalize_relative(path)
    target = resolve_path(requested)
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
        text_too_large=preview in {"text", "markdown"} and stat.st_size > settings.max_text_preview_bytes,
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
    return settings.root_resolved / CONFIG_NAME


def read_config() -> ConfigData:
    path = config_path()
    if not path.exists():
        return ConfigData()
    try:
        config = ConfigData.model_validate_json(path.read_text(encoding="utf-8"))
    except Exception:
        return ConfigData()
    try:
        current_path = normalize_relative(config.current_path)
        current_target = resolve_path(current_path)
    except HTTPException:
        current_path = ""
        current_target = settings.root_resolved
    if not current_target.exists() or not current_target.is_dir():
        current_path = ""
    return ConfigData(
        pinned=config.pinned,
        current_path=current_path,
        appearance=config.appearance,
        markdown=config.markdown,
    )


def write_config(config: ConfigData) -> ConfigData:
    path = config_path()
    path.write_text(config.model_dump_json(indent=2), encoding="utf-8")
    return config
