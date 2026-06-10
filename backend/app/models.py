from enum import StrEnum
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
    preview: Literal["image", "markdown", "html", "pdf", "text", "unsupported"]
    text_too_large: bool = False


class TextLineWindow(BaseModel):
    path: str
    size: int
    mtime: float
    total_lines: int
    start_line: int
    lines: list[str]
    truncated_start: bool = False
    truncated_end: bool = False


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
    proxy: str = ""
    muted_message_alpha: float = Field(default=0.56, ge=0.15, le=1.0)


class VoiceConfig(BaseModel):
    enabled: bool = True
    available_models: list[str] = Field(default_factory=lambda: ["large-v3-turbo", "small", "medium", "base", "tiny"])
    model: str = "large-v3-turbo"
    available_languages: list[str] = Field(default_factory=lambda: ["auto", "en", "zh", "ja", "ko", "fr", "de", "es"])
    language: str = "auto"
    translation_enabled: bool = False
    available_target_languages: list[str] = Field(default_factory=lambda: ["en", "zh", "ja", "ko", "fr", "de", "es"])
    target_language: str = "en"


class SuperWorkspaceConfig(BaseModel):
    hindsight_retain_enabled: bool = True
    hindsight_api_url: str = ""
    hindsight_bank_prefix: str = "super-workspace"
    chat_history_bootstrap_enabled: bool = True
    chat_history_bootstrap_tokens: int = Field(default=5000, ge=0, le=50000)


class UserProfile(BaseModel):
    id: str
    name: str = ""
    home: str = ""


class ConfigData(BaseModel):
    appearance: AppearanceConfig = Field(default_factory=AppearanceConfig)
    markdown: MarkdownConfig = Field(default_factory=MarkdownConfig)
    codex: CodexConfig = Field(default_factory=CodexConfig)
    voice: VoiceConfig = Field(default_factory=VoiceConfig)
    super_workspace: SuperWorkspaceConfig = Field(default_factory=SuperWorkspaceConfig)
    users: list[UserProfile] = Field(default_factory=list)
    default_user: str = ""


class WatchEvent(BaseModel):
    type: str
    path: str
    is_dir: bool
    mtime: float | None = None


class GitDiffFile(BaseModel):
    path: str
    status: str
    added: int | None = None
    deleted: int | None = None
    is_binary: bool = False


class GitStatus(BaseModel):
    files: list[GitDiffFile] = Field(default_factory=list)


class GitDiffText(BaseModel):
    path: str
    diff: str
    is_binary: bool = False


class GitStageRequest(BaseModel):
    path: str | None = None
    scope: str | None = None


class GitRevertRequest(BaseModel):
    path: str


class GitCommitRequest(BaseModel):
    message: str
    scope: str | None = None


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
    cwd_relative: str | None = None
    model: str | None = None
    created_at: float
    updated_at: float
    status: Literal["idle", "running", "exited", "failed"]
    exit_code: int | None = None
    pid: int | None = None
    codex_pid: int | None = None
    run_id: str | None = None
    run_started_at: float | None = None
    event_count: int = 0
    model_context_window: int | None = None
    context_used_percent: float | None = None
    total_tokens: int | None = None
    queue: list["AgentQueueItem"] = Field(default_factory=list)
    pending_approvals: list["AgentApproval"] = Field(default_factory=list)


class AgentPrompt(BaseModel):
    text: str
    created_at: float


class AgentFileChange(BaseModel):
    path: str
    change_type: str
    diff: str | None = None


class AgentEventType(StrEnum):
    MESSAGE_ASSISTANT = "message:assistant"
    REASONING = "reasoning"
    TOOL_CALL = "tool_call"
    TOOL_RESULT = "tool_result"
    CUSTOM_TOOL_CALL = "custom_tool_call"
    EXEC_COMMAND_BEGIN = "exec_command_begin"
    EXEC_COMMAND_END = "exec_command_end"
    FUNCTION_CALL = "function_call"
    FUNCTION_CALL_OUTPUT = "function_call_output"
    CUSTOM_TOOL_CALL_OUTPUT = "custom_tool_call_output"
    VIEW_IMAGE_TOOL_CALL = "view_image_tool_call"
    PATCH_APPLY_END = "patch_apply_end"
    OPERATION = "operation"


class AgentEvent(BaseModel):
    index: int
    received_at: float
    event_type: AgentEventType
    text: str = ""
    file_changes: list[AgentFileChange] = Field(default_factory=list)
    patch_text: str | None = None
    raw_preview: dict | None = None


class AgentQueueItem(BaseModel):
    id: str
    prompt: str
    created_at: float
    updated_at: float
    model: str | None = None


class AgentApproval(BaseModel):
    id: str
    provider: str
    session_id: str
    run_id: str | None = None
    title: str = "Approval required"
    description: str = ""
    command: str | None = None
    choices: list[str] = Field(default_factory=lambda: ["once", "session", "always", "deny"])
    created_at: float
    raw: dict | None = None


class CodexSessionSnapshot(CodexSessionInfo):
    prompts: list[AgentPrompt] = Field(default_factory=list)
    events: list[AgentEvent] = Field(default_factory=list)


class HermesSessionInfo(BaseModel):
    id: str
    hermes_session_id: str | None = None
    hermes_run_id: str | None = None
    db_path: str | None = None
    title: str
    cwd: str
    cwd_relative: str | None = None
    model: str | None = None
    created_at: float
    updated_at: float
    status: Literal["idle", "running", "exited", "failed"]
    exit_code: int | None = None
    event_count: int = 0
    total_tokens: int | None = None
    queue: list[AgentQueueItem] = Field(default_factory=list)
    pending_approvals: list[AgentApproval] = Field(default_factory=list)


class HermesSessionSnapshot(HermesSessionInfo):
    prompts: list[AgentPrompt] = Field(default_factory=list)
    events: list[AgentEvent] = Field(default_factory=list)


class ClientLog(BaseModel):
    level: Literal["debug", "info", "warning", "error"] = "error"
    message: str
    source: str = "frontend"
    stack: str | None = None
    url: str | None = None
    user_agent: str | None = None
