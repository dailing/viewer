from typing import Literal

from pydantic import BaseModel


class FileEntry(BaseModel):
    name: str
    path: str
    type: Literal["file", "directory", "symlink", "other"]
    size: int | None = None
    mtime: float | None = None
    mime: str | None = None
    is_dir: bool
    is_symlink: bool


class DirectoryListing(BaseModel):
    path: str
    entries: list[FileEntry]


class FileMeta(BaseModel):
    name: str
    path: str
    size: int
    mtime: float
    mime: str
    preview: Literal["image", "markdown", "pdf", "text", "unsupported"]
    text_too_large: bool = False


class ConfigData(BaseModel):
    pinned: list[str] = []
    current_path: str = ""


class WatchEvent(BaseModel):
    type: str
    path: str
    is_dir: bool
    mtime: float | None = None


class TerminalInfo(BaseModel):
    id: str
    title: str
    shell: str
    cwd: str
    created_at: float
    status: Literal["running", "exited"]
    exit_code: int | None = None


class TerminalCreate(BaseModel):
    cwd: str | None = None


class TerminalSnapshot(TerminalInfo):
    output: str
    output_version: int = 0


class ClientLog(BaseModel):
    level: Literal["debug", "info", "warning", "error"] = "error"
    message: str
    source: str = "frontend"
    stack: str | None = None
    url: str | None = None
    user_agent: str | None = None
