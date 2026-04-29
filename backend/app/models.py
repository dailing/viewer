from typing import Literal

from pydantic import BaseModel, Field


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
    content_hash: str
    mime: str
    preview: Literal["image", "markdown", "pdf", "text", "unsupported"]
    text_too_large: bool = False


class AppearanceConfig(BaseModel):
    navbar_size: int = 26


class MarkdownElementStyle(BaseModel):
    font_size: int | None = None
    color: str | None = None
    font_weight: str | None = None
    line_height: float | None = None


class MarkdownSyntaxStyle(BaseModel):
    background: str = "#f6f8fa"
    text: str = "#24292f"
    keyword: str = "#cf222e"
    string: str = "#0a3069"
    number: str = "#0550ae"
    title: str = "#8250df"
    comment: str = "#6e7781"
    meta: str = "#57606a"


class MarkdownTheme(BaseModel):
    name: str = "Default"
    body: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=15, color="#172033", line_height=1.65))
    h1: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=28, color="#172033", font_weight="700", line_height=1.2))
    h2: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=23, color="#172033", font_weight="700", line_height=1.25))
    h3: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=19, color="#172033", font_weight="700", line_height=1.3))
    h4: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=16, color="#172033", font_weight="700", line_height=1.35))
    paragraph: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=15, color="#172033", line_height=1.65))
    code: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=13, color="#24292f"))
    code_background: str = "#f6f8fa"
    link_color: str = "#0969da"
    border_color: str = "#d0d7de"
    syntax: MarkdownSyntaxStyle = Field(default_factory=MarkdownSyntaxStyle)


class MarkdownConfig(BaseModel):
    active_theme: str = "Default"
    themes: list[MarkdownTheme] = Field(default_factory=lambda: [MarkdownTheme()])


class ConfigData(BaseModel):
    pinned: list[str] = Field(default_factory=list)
    current_path: str = ""
    appearance: AppearanceConfig = Field(default_factory=AppearanceConfig)
    markdown: MarkdownConfig = Field(default_factory=MarkdownConfig)


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
    rows: int = 30
    cols: int = 120
    layout_locked: bool = False


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
