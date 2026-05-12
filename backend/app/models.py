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


DEFAULT_AUTO_COMMIT_PROMPT = """Review the Git changes in the current directory only.

Summarize the changes briefly, stage the relevant files in this directory, create a concise commit message, and commit them.

If a remote/tracking branch is configured, push the commit after it succeeds.

Do not amend, rebase, reset, or rewrite history. If there are no changes, unrelated changes outside this directory, or anything unsafe or unclear, stop and explain instead of committing."""


class CodexConfig(BaseModel):
    available_models: list[str] = Field(default_factory=lambda: ["gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.5"])
    default_model: str = "gpt-5.5"
    proxy: str = ""
    muted_message_alpha: float = Field(default=0.56, ge=0.15, le=1.0)
    auto_commit_prompt: str = DEFAULT_AUTO_COMMIT_PROMPT


class WorkspaceConfig(BaseModel):
    count: int = Field(default=5, ge=1, le=20)
    heat_interval_seconds: float = Field(default=10.0, ge=1.0, le=300.0)
    heat_step_percent: float = Field(default=5.0, ge=0.1, le=100.0)


class ConfigData(BaseModel):
    appearance: AppearanceConfig = Field(default_factory=AppearanceConfig)
    markdown: MarkdownConfig = Field(default_factory=MarkdownConfig)
    codex: CodexConfig = Field(default_factory=CodexConfig)
    workspace: WorkspaceConfig = Field(default_factory=WorkspaceConfig)


class WorkspaceSnapshot(BaseModel):
    layout: dict
    active_pane_id: str | None = None
    current_path: str = ""
    pinned: list[str] | None = None
    agent_session_ids: list[str] = Field(default_factory=list)
    codex_session_ids: list[str] = Field(default_factory=list)
    hermes_session_ids: list[str] = Field(default_factory=list)
    visit_times: dict[str, float] = Field(default_factory=dict)
    updated_at: float | None = None


class WorkspaceData(BaseModel):
    active_workspace_id: str = "1"
    count: int = Field(default=5, ge=1, le=20)
    slots: dict[str, WorkspaceSnapshot] = Field(default_factory=dict)


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
    queue: list["CodexQueueItem"] = Field(default_factory=list)


class CodexPrompt(BaseModel):
    text: str
    created_at: float


class CodexFileChange(BaseModel):
    path: str
    change_type: str
    diff: str | None = None


class CodexEvent(BaseModel):
    index: int
    received_at: float
    event_type: str
    text: str = ""
    file_changes: list[CodexFileChange] = Field(default_factory=list)
    patch_text: str | None = None
    raw_preview: dict | None = None


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


class CodexQueueItem(BaseModel):
    id: str
    prompt: str
    created_at: float
    updated_at: float
    model: str | None = None


class CodexQueueMessage(BaseModel):
    prompt: str
    model: str | None = None


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
    queue: list[CodexQueueItem] = Field(default_factory=list)


class HermesSessionSnapshot(HermesSessionInfo):
    prompts: list[CodexPrompt] = Field(default_factory=list)
    events: list[CodexEvent] = Field(default_factory=list)


class HermesSessionCreate(BaseModel):
    prompt: str = ""
    cwd: str | None = None
    model: str | None = None


class HermesSessionMessage(BaseModel):
    prompt: str
    model: str | None = None


class HermesQueueMessage(BaseModel):
    prompt: str
    model: str | None = None


class AgentSessionCreate(BaseModel):
    provider: str
    prompt: str = ""
    cwd: str | None = None
    model: str | None = None


class AgentSessionMessage(BaseModel):
    provider: str
    prompt: str
    model: str | None = None


class AgentQueueMessage(BaseModel):
    provider: str
    prompt: str
    model: str | None = None


class AgentProviderRequest(BaseModel):
    provider: str


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


class AgentLoopSchedule(BaseModel):
    type: Literal["manual", "once", "interval", "daily", "multi_daily"] = "manual"
    at_local: str | None = None
    start_at_local: str | None = None
    every_minutes: int = Field(default=30, ge=1)
    time_local: str = "09:00"
    times_local: list[str] = Field(default_factory=list)


class AgentLoopRunConfig(BaseModel):
    max_runs: int | None = Field(default=None, ge=1)
    max_consecutive_failures: int | None = Field(default=3, ge=1)
    skip_if_previous_running: bool = True


class AgentLoopSessionConfig(BaseModel):
    policy: Literal["new_each_run", "reuse", "reuse_until_context", "reuse_with_limits"] = "reuse_until_context"
    max_context_percent: float = Field(default=70, ge=1, le=100)
    reset_after_runs: int | None = Field(default=None, ge=1)
    reset_on_failure: bool = False


class AgentLoopStopConfig(BaseModel):
    final_message_regex: str | None = None


class AgentLoopDefinition(BaseModel):
    id: str
    name: str
    enabled: bool = True
    agent: Literal["codex"] = "codex"
    model: str | None = None
    cwd: str = ""
    timezone: str = "Asia/Shanghai"
    schedule: AgentLoopSchedule = Field(default_factory=AgentLoopSchedule)
    run: AgentLoopRunConfig = Field(default_factory=AgentLoopRunConfig)
    session: AgentLoopSessionConfig = Field(default_factory=AgentLoopSessionConfig)
    stop: AgentLoopStopConfig = Field(default_factory=AgentLoopStopConfig)
    prompt: str = ""


class AgentLoopRuntime(BaseModel):
    paused: bool = False
    stopped: bool = False
    stop_reason: str | None = None
    current_session_id: str | None = None
    current_run_id: str | None = None
    current_trigger: str | None = None
    run_count: int = 0
    session_run_count: int = 0
    consecutive_failures: int = 0
    last_run_at: float | None = None
    next_run_at: float | None = None
    last_status: str | None = None
    last_error: str | None = None


class AgentLoopInfo(BaseModel):
    definition: AgentLoopDefinition
    runtime: AgentLoopRuntime = Field(default_factory=AgentLoopRuntime)
    path: str
    parse_error: str | None = None


class AgentLoopRunRecord(BaseModel):
    run_id: str
    task_id: str
    task_name: str
    codex_session_id: str | None = None
    trigger: str = "schedule"
    model: str | None = None
    cwd: str = ""
    started_at: float
    finished_at: float | None = None
    status: str = "running"
    exit_code: int | None = None
    error: str | None = None
    prompt: str = ""
    session_snapshot: CodexSessionSnapshot | None = None


class AgentLoopCreate(BaseModel):
    name: str = "New Loop Task"


class AgentLoopRunRequest(BaseModel):
    trigger: str = "manual"


class ClientLog(BaseModel):
    level: Literal["debug", "info", "warning", "error"] = "error"
    message: str
    source: str = "frontend"
    stack: str | None = None
    url: str | None = None
    user_agent: str | None = None
