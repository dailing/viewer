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
    color_theme: Literal["system", "light", "dark"] = "system"
    density: Literal["compact", "comfortable"] = "compact"


class MarkdownElementStyle(BaseModel):
    font_size: int | None = None
    color: str | None = None
    font_weight: str | None = None
    line_height: float | None = None


class MarkdownSyntaxStyle(BaseModel):
    background: str = "#f5f5f5"
    text: str = "#4a4e53"
    keyword: str = "#8f5f63"
    string: str = "#55706b"
    number: str = "#627796"
    title: str = "#766b8c"
    comment: str = "#8a8e93"
    meta: str = "#72777c"


class MarkdownTheme(BaseModel):
    name: str = "Default"
    body: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=15, color="#404449", line_height=1.65))
    h1: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=28, color="#30343a", font_weight="700", line_height=1.2))
    h2: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=23, color="#30343a", font_weight="700", line_height=1.25))
    h3: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=19, color="#34383d", font_weight="700", line_height=1.3))
    h4: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=16, color="#34383d", font_weight="700", line_height=1.35))
    paragraph: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=15, color="#404449", line_height=1.65))
    code: MarkdownElementStyle = Field(default_factory=lambda: MarkdownElementStyle(font_size=13, color="#4a4e53"))
    code_background: str = "#f5f5f5"
    link_color: str = "#58749a"
    border_color: str = "#e3e4e6"
    syntax: MarkdownSyntaxStyle = Field(default_factory=MarkdownSyntaxStyle)


class MarkdownConfig(BaseModel):
    active_theme: str = "Default"
    follow_app_theme: bool = True
    themes: list[MarkdownTheme] = Field(default_factory=lambda: [MarkdownTheme()])


class CodexConfig(BaseModel):
    available_models: list[str] = Field(default_factory=lambda: ["gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.3-codex", "gpt-5.3-codex-spark"])
    default_model: str = "gpt-5.5"
    proxy: str = ""
    muted_message_alpha: float = Field(default=0.56, ge=0.15, le=1.0)


class VoiceConfig(BaseModel):
    enabled: bool = True
    language_model_refine: bool = True
    available_models: list[str] = Field(default_factory=lambda: ["large-v3-turbo", "small", "medium", "base", "tiny"])
    model: str = "large-v3-turbo"
    available_languages: list[str] = Field(default_factory=lambda: ["auto", "en", "zh", "ja", "ko", "fr", "de", "es"])
    language: str = "auto"
    translation_enabled: bool = False
    available_target_languages: list[str] = Field(default_factory=lambda: ["en", "zh", "ja", "ko", "fr", "de", "es"])
    target_language: str = "en"


class SuperWorkspaceDispatchProfile(BaseModel):
    id: str = "local-vllm"
    name: str = "Local vLLM"
    api_url: str = "http://127.0.0.1:8010/v1/chat/completions"
    model: str = "qwen3-14b"
    api_key: str = ""


DEFAULT_DISPATCH_PROMPT_TEMPLATE = """You route one user message to persistent agent roles.

Default to exactly one role.
Choose multiple roles only when the user explicitly asks for multiple independent tasks, or when no single role can reasonably complete the request.

Use recent visible chat history only to understand context, not as a separate task.
Return only JSON:
{"role_ids":["role-id"],"rationale":"short reason"}

Available roles:
{{roles_json}}

Recent visible chat history:
{{history}}

Current message:
{{message}}"""


class SuperWorkspaceConfig(BaseModel):
    hindsight_retain_enabled: bool = True
    hindsight_api_url: str = ""
    hindsight_bank_prefix: str = "super-workspace"
    chat_history_bootstrap_enabled: bool = True
    chat_history_bootstrap_tokens: int = Field(default=5000, ge=0, le=50000)
    active_dispatch_profile_id: str = "local-vllm"
    dispatch_history_word_budget: int = Field(default=2048, ge=0, le=50000)
    dispatch_prompt_template: str = DEFAULT_DISPATCH_PROMPT_TEMPLATE
    dispatch_profiles: list[SuperWorkspaceDispatchProfile] = Field(
        default_factory=lambda: [
            SuperWorkspaceDispatchProfile(
                id="local-vllm",
                name="Local vLLM",
                api_url="http://127.0.0.1:8010/v1/chat/completions",
                model="qwen3-14b",
                api_key="",
            ),
            SuperWorkspaceDispatchProfile(
                id="deepseek",
                name="DeepSeek",
                api_url="https://api.deepseek.com/v1/chat/completions",
                model="deepseek-v4-flash",
                api_key="",
            ),
        ]
    )


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
