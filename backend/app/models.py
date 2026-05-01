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


class CodexConfig(BaseModel):
    available_models: list[str] = Field(default_factory=lambda: ["gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.5"])
    default_model: str = "gpt-5.5"


class WorkspaceConfig(BaseModel):
    count: int = Field(default=5, ge=1, le=20)


class ConfigData(BaseModel):
    pinned: list[str] = Field(default_factory=list)
    current_path: str = ""
    visit_times: dict[str, float] = Field(default_factory=dict)
    appearance: AppearanceConfig = Field(default_factory=AppearanceConfig)
    markdown: MarkdownConfig = Field(default_factory=MarkdownConfig)
    codex: CodexConfig = Field(default_factory=CodexConfig)
    workspaces: WorkspaceConfig = Field(default_factory=WorkspaceConfig)


class WorkspaceSnapshot(BaseModel):
    layout: dict
    active_pane_id: str | None = None
    current_path: str = ""
    pinned: list[str] | None = None
    updated_at: float | None = None


class WorkspaceData(BaseModel):
    active_workspace_id: str = "1"
    slots: dict[str, WorkspaceSnapshot] = Field(default_factory=dict)


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


class CodexSessionInfo(BaseModel):
    id: str
    codex_session_id: str | None = None
    rollout_path: str | None = None
    title: str
    cwd: str
    model: str | None = None
    created_at: float
    updated_at: float
    status: Literal["idle", "running", "exited", "failed"]
    exit_code: int | None = None
    event_count: int = 0
    model_context_window: int | None = None
    context_used_percent: float | None = None
    total_tokens: int | None = None


class CodexPrompt(BaseModel):
    text: str
    created_at: float


class CodexEvent(BaseModel):
    index: int
    received_at: float
    raw: dict


class CodexSessionSnapshot(CodexSessionInfo):
    prompts: list[CodexPrompt] = Field(default_factory=list)
    events: list[CodexEvent] = Field(default_factory=list)


class CodexSessionCreate(BaseModel):
    prompt: str = ""
    cwd: str | None = None
    model: str | None = None


class CodexSessionMessage(BaseModel):
    prompt: str
    model: str | None = None


class CodexCliStatus(BaseModel):
    available: bool = False
    session_id: str | None = None
    rollout_path: str | None = None
    updated_at: float | None = None
    cwd: str | None = None
    model: str | None = None
    model_context_window: int | None = None
    context_used_percent: float | None = None
    total_tokens: int | None = None
    plan_type: str | None = None
    primary_used_percent: float | None = None
    primary_remaining_percent: float | None = None
    primary_window_minutes: int | None = None
    secondary_used_percent: float | None = None
    secondary_remaining_percent: float | None = None
    secondary_window_minutes: int | None = None


class CodexModelOptions(BaseModel):
    selected_model: str
    available_models: list[str] = Field(default_factory=list)
    source: str = "config"


class ClientLog(BaseModel):
    level: Literal["debug", "info", "warning", "error"] = "error"
    message: str
    source: str = "frontend"
    stack: str | None = None
    url: str | None = None
    user_agent: str | None = None
