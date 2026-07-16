from __future__ import annotations

import json
import os
import re
import sqlite3
import time
import uuid
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path
from typing import Any

from pydantic import BaseModel, Field
from sqlalchemy import Float, ForeignKey, Index, Integer, String, Text, UniqueConstraint, and_, create_engine, delete, event, or_, select, update
from sqlalchemy.dialects.sqlite import insert as sqlite_insert
from sqlalchemy.orm import DeclarativeBase, Mapped, Session, mapped_column, sessionmaker
from sqlalchemy.pool import NullPool

from .storage import AGENT_HISTORY_DB_PATH
from .super_workspace_memory import retain_visible_message_background
from .users import normalize_user_id

DEFAULT_SUPER_WORKSPACE_ID = "default"
DEFAULT_SUPER_WORKSPACE_NAME = "Default Super Workspace"
DEFAULT_RUN_LIMIT = 30
DEFAULT_MESSAGE_LIMIT = 120
ROLE_SESSION_POLICIES = {"reuse", "new_each_run"}
DEFAULT_CONTEXT_RECYCLE_PERCENT = 70.0
STALE_DRIVER_RUN_GRACE_SECONDS = 120.0
TOKENISH_RE = re.compile(r"\w+|[^\w\s]", re.UNICODE)
CHAT_ID_RE = re.compile(r"^[0-9a-f]{12}$")


def rough_token_count(value: str) -> int:
    return len(TOKENISH_RE.findall(value))


def trim_to_rough_tokens(value: str, limit: int) -> str:
    if limit <= 0:
        return ""
    matches = list(TOKENISH_RE.finditer(value))
    if len(matches) <= limit:
        return value
    return value[: matches[limit - 1].end()].rstrip()


def normalize_relative_cwd(value: Any) -> str:
    cwd = str(value or "").strip()
    if not cwd:
        return ""
    if cwd.startswith("/"):
        raise ValueError("cwd must be relative to the profile home")
    if "\\" in cwd:
        raise ValueError("cwd must use forward slashes")
    parts = [part for part in cwd.split("/") if part]
    if any(part == ".." for part in parts):
        raise ValueError("cwd cannot contain .. path segments")
    return "/".join(parts)


class SuperHistoryRunCreate(BaseModel):
    message: str
    chat_id: str | None = None
    role_ids: list[str] | None = None
    parent_message_id: str | None = None
    sender_role_id: str | None = None


class SuperDriverRunCreate(BaseModel):
    workspace_id: str = DEFAULT_SUPER_WORKSPACE_ID
    chat_id: str | None = None
    role_id: str
    role_name: str
    provider: str
    session_ref: str = ""
    agent_prompt: str = ""
    parent_message_id: str | None = None
    sender_role_id: str | None = None
    role_snapshot: dict[str, Any] = Field(default_factory=dict)


class SuperDispatchTask(BaseModel):
    id: str
    workspace_id: str
    chat_id: str = ""
    query_message_id: str
    user_id: str
    role_id: str
    role_name: str
    provider: str
    viewer_session_id: str = ""
    provider_session_id: str | None = None
    session_ref: str = ""
    agent_prompt: str = ""
    status: str
    parent_message_id: str | None = None
    sender_role_id: str | None = None
    recipient_role_id: str
    role_snapshot: dict[str, Any] = Field(default_factory=dict)
    claimed_by: str | None = None
    claim_expires_at: float | None = None
    attempt_count: int = 0
    next_attempt_at: float | None = None
    driver_pid: int | None = None
    driver_state_path: str | None = None
    error: str = ""
    start_after_occurred_at: float = 0
    created_at: float
    started_at: float | None = None
    finished_at: float | None = None
    updated_at: float


class AgentHistoryFileChange(BaseModel):
    path: str
    change_type: str
    diff: str | None = None


class AgentHistoryMessage(BaseModel):
    id: str
    workspace_id: str | None = None
    chat_id: str | None = None
    provider: str
    viewer_session_id: str | None = None
    provider_session_id: str | None = None
    index: int
    received_at: float
    role: str
    event_type: str
    text: str = ""
    query: str | None = None
    status: str | None = None
    rationale: str = ""
    error: str = ""
    requested_role_ids: list[str] = Field(default_factory=list)
    selected_role_ids: list[str] = Field(default_factory=list)
    file_changes: list[AgentHistoryFileChange] = Field(default_factory=list)
    patch_text: str | None = None
    raw: dict[str, Any] = Field(default_factory=dict)
    occurred_at: float
    query_id: str | None = None
    query_message_id: str | None = None
    driver_run_id: str | None = None
    super_run_id: str | None = None
    super_target_id: str | None = None
    parent_message_id: str | None = None
    sender_role_id: str | None = None
    recipient_role_id: str | None = None


class SuperHistoryTarget(BaseModel):
    id: str
    workspace_id: str
    chat_id: str = ""
    run_id: str
    role_id: str
    role_name: str
    provider: str
    viewer_session_id: str
    provider_session_id: str | None = None
    session_ref: str
    agent_prompt: str = ""
    status: str = "queued"
    model_context_window: int | None = None
    total_tokens: int | None = None
    context_used_percent: float | None = None
    created_at: float
    updated_at: float
    messages: list[AgentHistoryMessage] = Field(default_factory=list)


class SuperHistoryRun(BaseModel):
    id: str
    workspace_id: str
    chat_id: str = ""
    user_id: str
    message: str
    query: str
    message_id: str
    role_ids: list[str] = Field(default_factory=list)
    citation_ids: list[str] = Field(default_factory=list)
    status: str
    rationale: str = ""
    error: str = ""
    parent_message_id: str | None = None
    sender_role_id: str | None = None
    created_at: float
    updated_at: float
    targets: list[SuperHistoryTarget] = Field(default_factory=list)


class SuperDisplayTarget(BaseModel):
    id: str
    workspace_id: str
    chat_id: str = ""
    role_id: str
    role_name: str
    provider: str
    viewer_session_id: str = ""
    provider_session_id: str | None = None
    session_ref: str
    status: str
    model_context_window: int | None = None
    total_tokens: int | None = None
    context_used_percent: float | None = None


class SuperDisplayItem(BaseModel):
    id: str
    workspace_id: str
    chat_id: str
    kind: str
    user_id: str
    text: str
    role: str
    event_type: str
    provider: str
    created_at: float
    updated_at: float
    message_id: str
    query_message_id: str | None = None
    driver_run_id: str | None = None
    parent_message_id: str | None = None
    sender_role_id: str | None = None
    recipient_role_id: str | None = None
    role_id: str | None = None
    role_name: str = ""
    viewer_session_id: str = ""
    provider_session_id: str | None = None
    session_ref: str = ""
    model_context_window: int | None = None
    total_tokens: int | None = None
    context_used_percent: float | None = None
    target_status: str = ""
    run_status: str = ""
    error: str = ""
    citation_ids: list[str] = Field(default_factory=list)
    dispatch_targets: list[SuperDisplayTarget] = Field(default_factory=list)
    raw: dict[str, Any] = Field(default_factory=dict)


class SuperDisplayItemsPage(BaseModel):
    items: list[SuperDisplayItem]
    has_more: bool = False
    next_before: float | None = None
    next_after: float | None = None


class SuperChatRoleSessionState(BaseModel):
    id: str
    workspace_id: str
    chat_id: str
    user_id: str
    role_id: str
    provider: str
    session_ref: str = ""
    viewer_session_id: str = ""
    provider_session_id: str | None = None
    cwd: str = ""
    model: str | None = None
    session_policy_snapshot: str = "reuse"
    model_context_window: int | None = None
    total_tokens: int | None = None
    context_used_percent: float | None = None
    usage_updated_at: float | None = None
    last_driver_run_id: str = ""
    last_message_id: str = ""
    rotation_count: int = 0
    last_rotation_reason: str = ""
    created_at: float
    updated_at: float


class SuperHistoryRunsPage(BaseModel):
    runs: list[SuperHistoryRun]
    has_more: bool = False
    next_before: float | None = None
    next_after: float | None = None


class SuperWorkspaceSummary(BaseModel):
    id: str
    name: str
    created_at: float
    updated_at: float


class SuperWorkspaceList(BaseModel):
    active_workspace_id: str
    workspaces: list[SuperWorkspaceSummary]


class SuperChatSummary(BaseModel):
    id: str
    workspace_id: str
    name: str
    type: str = "group"
    pinned: bool = False
    cwd: str = ""
    common_prompt: str = ""
    member_role_ids: list[str] = Field(default_factory=list)
    created_at: float
    updated_at: float


class SuperChatList(BaseModel):
    active_chat_id: str
    chats: list[SuperChatSummary]


class AgentHistoryBase(DeclarativeBase):
    pass


class SuperWorkspaceRow(AgentHistoryBase):
    __tablename__ = "super_workspaces"
    __table_args__ = (
        UniqueConstraint("user_id", "id"),
        Index("idx_super_workspaces_user", "user_id", "updated_at"),
    )

    id: Mapped[str] = mapped_column(String, primary_key=True)
    user_id: Mapped[str] = mapped_column(String, nullable=False)
    name: Mapped[str] = mapped_column(String, nullable=False)
    common_prompt: Mapped[str] = mapped_column(Text, nullable=False, default="")
    created_at: Mapped[float] = mapped_column(Float, nullable=False)
    updated_at: Mapped[float] = mapped_column(Float, nullable=False)


class SuperWorkspaceUserStateRow(AgentHistoryBase):
    __tablename__ = "super_workspace_user_state"

    user_id: Mapped[str] = mapped_column(String, primary_key=True)
    active_workspace_id: Mapped[str] = mapped_column(String, ForeignKey("super_workspaces.id", ondelete="CASCADE"), nullable=False)
    active_chat_id: Mapped[str] = mapped_column(String, nullable=False, default="")
    updated_at: Mapped[float] = mapped_column(Float, nullable=False)


class SuperWorkspaceChatRow(AgentHistoryBase):
    __tablename__ = "super_workspace_chats"
    __table_args__ = (
        UniqueConstraint("workspace_id", "user_id", "id"),
        Index("idx_super_chats_workspace", "user_id", "workspace_id", "created_at", "id"),
    )

    id: Mapped[str] = mapped_column(String, primary_key=True)
    workspace_id: Mapped[str] = mapped_column(String, ForeignKey("super_workspaces.id", ondelete="CASCADE"), primary_key=True)
    user_id: Mapped[str] = mapped_column(String, primary_key=True)
    name: Mapped[str] = mapped_column(String, nullable=False)
    type: Mapped[str] = mapped_column(String, nullable=False, default="group")
    cwd: Mapped[str] = mapped_column(Text, nullable=False, default="")
    common_prompt: Mapped[str] = mapped_column(Text, nullable=False, default="")
    member_role_ids_json: Mapped[str] = mapped_column(Text, nullable=False, default="[]")
    created_at: Mapped[float] = mapped_column(Float, nullable=False)
    updated_at: Mapped[float] = mapped_column(Float, nullable=False)


class SuperWorkspaceChatPinRow(AgentHistoryBase):
    __tablename__ = "super_workspace_chat_pins"
    __table_args__ = (
        Index("idx_super_chat_pins_workspace", "user_id", "workspace_id", "created_at", "chat_id"),
    )

    user_id: Mapped[str] = mapped_column(String, primary_key=True)
    workspace_id: Mapped[str] = mapped_column(String, ForeignKey("super_workspaces.id", ondelete="CASCADE"), primary_key=True)
    chat_id: Mapped[str] = mapped_column(String, primary_key=True)
    created_at: Mapped[float] = mapped_column(Float, nullable=False)
    updated_at: Mapped[float] = mapped_column(Float, nullable=False)


class SuperWorkspaceRoleRow(AgentHistoryBase):
    __tablename__ = "super_workspace_roles"
    __table_args__ = (
        Index("idx_super_roles_workspace", "user_id", "workspace_id", "created_at", "id"),
    )

    id: Mapped[str] = mapped_column(String, primary_key=True)
    workspace_id: Mapped[str] = mapped_column(String, ForeignKey("super_workspaces.id", ondelete="CASCADE"), nullable=False)
    user_id: Mapped[str] = mapped_column(String, nullable=False)
    name: Mapped[str] = mapped_column(String, nullable=False)
    description: Mapped[str] = mapped_column(Text, nullable=False, default="")
    provider: Mapped[str] = mapped_column(String, nullable=False, default="codex")
    cwd: Mapped[str] = mapped_column(Text, nullable=False, default="")
    model: Mapped[str | None] = mapped_column(String)
    session_policy: Mapped[str] = mapped_column(String, nullable=False, default="reuse")
    created_at: Mapped[float] = mapped_column(Float, nullable=False)
    updated_at: Mapped[float] = mapped_column(Float, nullable=False)


class SuperWorkspaceChatRoleSessionRow(AgentHistoryBase):
    __tablename__ = "super_workspace_chat_role_sessions"
    __table_args__ = (
        UniqueConstraint("user_id", "workspace_id", "chat_id", "role_id", "provider"),
        Index("idx_super_chat_role_sessions_lookup", "user_id", "workspace_id", "chat_id", "role_id", "provider"),
        Index("idx_super_chat_role_sessions_session", "provider", "viewer_session_id"),
    )

    id: Mapped[str] = mapped_column(String, primary_key=True)
    workspace_id: Mapped[str] = mapped_column(String, ForeignKey("super_workspaces.id", ondelete="CASCADE"), nullable=False)
    chat_id: Mapped[str] = mapped_column(String, nullable=False)
    user_id: Mapped[str] = mapped_column(String, nullable=False)
    role_id: Mapped[str] = mapped_column(String, nullable=False)
    provider: Mapped[str] = mapped_column(String, nullable=False)
    session_ref: Mapped[str] = mapped_column(String, nullable=False, default="")
    viewer_session_id: Mapped[str] = mapped_column(String, nullable=False, default="")
    provider_session_id: Mapped[str | None] = mapped_column(String)
    cwd: Mapped[str] = mapped_column(Text, nullable=False, default="")
    model: Mapped[str | None] = mapped_column(String)
    session_policy_snapshot: Mapped[str] = mapped_column(String, nullable=False, default="reuse")
    model_context_window: Mapped[int | None] = mapped_column(Integer)
    total_tokens: Mapped[int | None] = mapped_column(Integer)
    context_used_percent: Mapped[float | None] = mapped_column(Float)
    usage_updated_at: Mapped[float | None] = mapped_column(Float)
    last_driver_run_id: Mapped[str] = mapped_column(String, nullable=False, default="")
    last_message_id: Mapped[str] = mapped_column(String, nullable=False, default="")
    rotation_count: Mapped[int] = mapped_column(Integer, nullable=False, default=0)
    last_rotation_reason: Mapped[str] = mapped_column(String, nullable=False, default="")
    created_at: Mapped[float] = mapped_column(Float, nullable=False)
    updated_at: Mapped[float] = mapped_column(Float, nullable=False)


class SuperWorkspaceMessageRow(AgentHistoryBase):
    __tablename__ = "super_workspace_messages"
    __table_args__ = (
        UniqueConstraint("provider", "viewer_session_id", "source_event_id"),
        Index("idx_super_messages_workspace_user_chat_time", "workspace_id", "user_id", "conversation_id", "occurred_at", "id"),
        Index("idx_super_messages_user_query_time", "user_id", "query", "occurred_at", "id"),
        Index("idx_super_messages_query_message", "query_message_id", "occurred_at", "id"),
        Index("idx_super_messages_driver_time", "driver_run_id", "occurred_at", "id"),
        Index("idx_super_messages_session_time", "provider", "viewer_session_id", "occurred_at", "id"),
    )

    id: Mapped[str] = mapped_column(String, primary_key=True)
    workspace_id: Mapped[str | None] = mapped_column(String, ForeignKey("super_workspaces.id", ondelete="CASCADE"))
    user_id: Mapped[str] = mapped_column(String, nullable=False)
    conversation_id: Mapped[str] = mapped_column(String, nullable=False)
    parent_message_id: Mapped[str | None] = mapped_column(String)
    sender_role_id: Mapped[str | None] = mapped_column(String)
    recipient_role_id: Mapped[str | None] = mapped_column(String)
    role_id: Mapped[str | None] = mapped_column(String)
    query_message_id: Mapped[str | None] = mapped_column(String)
    driver_run_id: Mapped[str | None] = mapped_column(String)
    provider: Mapped[str] = mapped_column(String, nullable=False)
    viewer_session_id: Mapped[str | None] = mapped_column(String)
    provider_session_id: Mapped[str | None] = mapped_column(String)
    event_index: Mapped[int] = mapped_column(Integer, nullable=False)
    received_at: Mapped[float] = mapped_column(Float, nullable=False)
    source_path: Mapped[str | None] = mapped_column(Text)
    source_event_id: Mapped[str] = mapped_column(String, nullable=False)
    source_line: Mapped[int | None] = mapped_column(Integer)
    role: Mapped[str] = mapped_column(String, nullable=False)
    event_type: Mapped[str] = mapped_column(String, nullable=False)
    text: Mapped[str] = mapped_column(Text, nullable=False)
    query: Mapped[str | None] = mapped_column(Text)
    status: Mapped[str | None] = mapped_column(String)
    rationale: Mapped[str] = mapped_column(Text, nullable=False, default="")
    error: Mapped[str] = mapped_column(Text, nullable=False, default="")
    requested_role_ids_json: Mapped[str] = mapped_column(Text, nullable=False, default="[]")
    selected_role_ids_json: Mapped[str] = mapped_column(Text, nullable=False, default="[]")
    patch_text: Mapped[str | None] = mapped_column(Text)
    raw_json: Mapped[str] = mapped_column(Text, nullable=False)
    occurred_at: Mapped[float] = mapped_column(Float, nullable=False)
    ingested_at: Mapped[float] = mapped_column(Float, nullable=False)


class SuperWorkspaceDriverRunRow(AgentHistoryBase):
    __tablename__ = "super_workspace_driver_runs"
    __table_args__ = (
        Index("idx_super_driver_runs_query_message", "query_message_id", "created_at", "id"),
        Index("idx_super_driver_runs_role_status", "user_id", "workspace_id", "role_id", "status", "updated_at"),
    )

    id: Mapped[str] = mapped_column(String, primary_key=True)
    workspace_id: Mapped[str] = mapped_column(String, ForeignKey("super_workspaces.id", ondelete="CASCADE"), nullable=False)
    query_message_id: Mapped[str] = mapped_column(String, ForeignKey("super_workspace_messages.id", ondelete="CASCADE"), nullable=False)
    user_id: Mapped[str] = mapped_column(String, nullable=False)
    role_id: Mapped[str] = mapped_column(String, nullable=False)
    role_name: Mapped[str] = mapped_column(String, nullable=False)
    provider: Mapped[str] = mapped_column(String, nullable=False)
    viewer_session_id: Mapped[str] = mapped_column(String, nullable=False, default="")
    provider_session_id: Mapped[str | None] = mapped_column(String)
    session_ref: Mapped[str] = mapped_column(String, nullable=False, default="")
    agent_prompt: Mapped[str] = mapped_column(Text, nullable=False)
    status: Mapped[str] = mapped_column(String, nullable=False)
    model_context_window: Mapped[int | None] = mapped_column(Integer)
    total_tokens: Mapped[int | None] = mapped_column(Integer)
    context_used_percent: Mapped[float | None] = mapped_column(Float)
    parent_message_id: Mapped[str | None] = mapped_column(String)
    sender_role_id: Mapped[str | None] = mapped_column(String)
    recipient_role_id: Mapped[str] = mapped_column(String, nullable=False)
    role_snapshot_json: Mapped[str] = mapped_column(Text, nullable=False, default="{}")
    claimed_by: Mapped[str | None] = mapped_column(String)
    claim_expires_at: Mapped[float | None] = mapped_column(Float)
    attempt_count: Mapped[int] = mapped_column(Integer, nullable=False, default=0)
    next_attempt_at: Mapped[float | None] = mapped_column(Float)
    driver_pid: Mapped[int | None] = mapped_column(Integer)
    driver_state_path: Mapped[str | None] = mapped_column(Text)
    error: Mapped[str] = mapped_column(Text, nullable=False, default="")
    start_after_occurred_at: Mapped[float] = mapped_column(Float, nullable=False)
    created_at: Mapped[float] = mapped_column(Float, nullable=False)
    started_at: Mapped[float | None] = mapped_column(Float)
    finished_at: Mapped[float | None] = mapped_column(Float)
    updated_at: Mapped[float] = mapped_column(Float, nullable=False)


class SuperWorkspaceMessageFileChangeRow(AgentHistoryBase):
    __tablename__ = "super_workspace_message_file_changes"

    message_id: Mapped[str] = mapped_column(String, ForeignKey("super_workspace_messages.id", ondelete="CASCADE"), primary_key=True)
    position: Mapped[int] = mapped_column(Integer, primary_key=True)
    path: Mapped[str] = mapped_column(Text, nullable=False)
    change_type: Mapped[str] = mapped_column(String, nullable=False)
    diff: Mapped[str | None] = mapped_column(Text)


class SuperWorkspaceMessageCitationRow(AgentHistoryBase):
    __tablename__ = "super_workspace_message_citations"
    __table_args__ = (
        Index("idx_super_citations_source", "source_message_id", "position"),
        Index("idx_super_citations_cited", "cited_message_id"),
    )

    source_message_id: Mapped[str] = mapped_column(String, ForeignKey("super_workspace_messages.id", ondelete="CASCADE"), primary_key=True)
    position: Mapped[int] = mapped_column(Integer, primary_key=True)
    cited_message_id: Mapped[str] = mapped_column(String, ForeignKey("super_workspace_messages.id", ondelete="CASCADE"), nullable=False)
    created_at: Mapped[float] = mapped_column(Float, nullable=False)


class SuperWorkspaceDriverCheckpointRow(AgentHistoryBase):
    __tablename__ = "super_workspace_driver_checkpoints"

    driver_run_id: Mapped[str] = mapped_column(String, ForeignKey("super_workspace_driver_runs.id", ondelete="CASCADE"), primary_key=True)
    provider: Mapped[str] = mapped_column(String, nullable=False)
    viewer_session_id: Mapped[str] = mapped_column(String, nullable=False)
    source_path: Mapped[str | None] = mapped_column(Text)
    byte_offset: Mapped[int] = mapped_column(Integer, nullable=False, default=0)
    line_count: Mapped[int] = mapped_column(Integer, nullable=False, default=0)
    last_source_event_id: Mapped[str | None] = mapped_column(String)
    updated_at: Mapped[float] = mapped_column(Float, nullable=False)


class AgentHistoryStore:
    def __init__(self, path: Path = AGENT_HISTORY_DB_PATH) -> None:
        self.path = path
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.engine = create_engine(
            f"sqlite:///{self.path.as_posix()}",
            future=True,
            poolclass=NullPool,
            connect_args={"timeout": 30.0},
        )
        event.listen(self.engine, "connect", self._configure_sqlite_connection)
        self.SessionLocal = sessionmaker(self.engine, expire_on_commit=False, future=True)
        self._ensure_sqlite_wal()
        self._ensure_schema()

    @staticmethod
    def _configure_sqlite_connection(connection, _record) -> None:
        cursor = connection.cursor()
        try:
            cursor.execute("PRAGMA synchronous=NORMAL")
            cursor.execute("PRAGMA busy_timeout=30000")
        finally:
            cursor.close()

    def _ensure_sqlite_wal(self) -> None:
        with self.engine.connect() as connection:
            connection.exec_driver_sql("PRAGMA journal_mode=WAL")

    def _table_names(self, connection) -> set[str]:
        return {str(row[0]) for row in connection.exec_driver_sql("SELECT name FROM sqlite_master WHERE type = 'table'").all()}

    def _table_columns(self, connection, table: str) -> set[str]:
        if table not in self._table_names(connection):
            return set()
        return {str(row[1]) for row in connection.exec_driver_sql(f"PRAGMA table_info({table})").all()}

    def _schema_needs_backup(self) -> bool:
        if not self.path.exists():
            return False
        with self.engine.connect() as connection:
            tables = self._table_names(connection)
            if not tables:
                return False
            if "super_workspace_roles" in tables and "session_ref" in self._table_columns(connection, "super_workspace_roles"):
                return True
            if "super_workspace_chat_role_sessions" not in tables and "super_workspace_roles" in tables:
                return True
            if "pinned" in self._table_columns(connection, "super_workspace_chats"):
                return True
            driver_columns = self._table_columns(connection, "super_workspace_driver_runs")
            return bool(driver_columns) and not {"model_context_window", "total_tokens", "context_used_percent"}.issubset(driver_columns)

    def _backup_database_for_schema_migration(self) -> None:
        timestamp = time.strftime("%Y%m%d%H%M%S", time.gmtime())
        backup_path = self.path.with_name(f"{self.path.name}.backup-{timestamp}")
        source = sqlite3.connect(f"file:{self.path.as_posix()}?mode=ro", uri=True)
        target = sqlite3.connect(backup_path.as_posix())
        try:
            source.backup(target)
        finally:
            target.close()
            source.close()

    @contextmanager
    def session_scope(self) -> Iterator[Session]:
        session = self.SessionLocal()
        try:
            yield session
            session.commit()
        except Exception:
            session.rollback()
            raise
        finally:
            session.close()

    @contextmanager
    def read_session(self) -> Iterator[Session]:
        session = self.SessionLocal()
        try:
            yield session
        finally:
            session.close()

    def _ensure_schema(self) -> None:
        needs_backup = self._schema_needs_backup()
        if needs_backup:
            self._backup_database_for_schema_migration()
        AgentHistoryBase.metadata.create_all(self.engine)
        with self.engine.begin() as connection:
            columns = {row[1] for row in connection.exec_driver_sql("PRAGMA table_info(super_workspace_user_state)").all()}
            if "active_chat_id" not in columns:
                connection.exec_driver_sql("ALTER TABLE super_workspace_user_state ADD COLUMN active_chat_id VARCHAR NOT NULL DEFAULT ''")
            chat_columns = {row[1] for row in connection.exec_driver_sql("PRAGMA table_info(super_workspace_chats)").all()}
            if "cwd" not in chat_columns:
                connection.exec_driver_sql("ALTER TABLE super_workspace_chats ADD COLUMN cwd TEXT NOT NULL DEFAULT ''")
            if "member_role_ids_json" not in chat_columns:
                connection.exec_driver_sql("ALTER TABLE super_workspace_chats ADD COLUMN member_role_ids_json TEXT NOT NULL DEFAULT '[]'")
            if "pinned" in chat_columns:
                connection.exec_driver_sql("ALTER TABLE super_workspace_chats DROP COLUMN pinned")
            role_columns = {row[1] for row in connection.exec_driver_sql("PRAGMA table_info(super_workspace_roles)").all()}
            if "session_policy" not in role_columns:
                connection.exec_driver_sql("ALTER TABLE super_workspace_roles ADD COLUMN session_policy VARCHAR NOT NULL DEFAULT 'reuse'")
            driver_columns = {row[1] for row in connection.exec_driver_sql("PRAGMA table_info(super_workspace_driver_runs)").all()}
            if "model_context_window" not in driver_columns:
                connection.exec_driver_sql("ALTER TABLE super_workspace_driver_runs ADD COLUMN model_context_window INTEGER")
            if "total_tokens" not in driver_columns:
                connection.exec_driver_sql("ALTER TABLE super_workspace_driver_runs ADD COLUMN total_tokens INTEGER")
            if "context_used_percent" not in driver_columns:
                connection.exec_driver_sql("ALTER TABLE super_workspace_driver_runs ADD COLUMN context_used_percent FLOAT")
            self._migrate_role_session_refs(connection)
            self._migrate_invalid_super_chat_ids(connection)
            role_columns = {row[1] for row in connection.exec_driver_sql("PRAGMA table_info(super_workspace_roles)").all()}
            if "session_ref" in role_columns:
                connection.exec_driver_sql("ALTER TABLE super_workspace_roles DROP COLUMN session_ref")
        for index in SuperWorkspaceDriverRunRow.__table__.indexes:
            index.create(self.engine, checkfirst=True)

    def _migrate_role_session_refs(self, connection) -> None:
        role_columns = {row[1] for row in connection.exec_driver_sql("PRAGMA table_info(super_workspace_roles)").all()}
        if "session_ref" not in role_columns:
            return
        now = time.time()
        rows = connection.exec_driver_sql(
            """
            SELECT id, workspace_id, user_id, provider, cwd, model, session_ref, session_policy
            FROM super_workspace_roles
            WHERE session_ref IS NOT NULL AND session_ref != ''
            """
        ).mappings().all()
        for row in rows:
            chat_id = connection.exec_driver_sql(
                """
                SELECT c.id
                FROM super_workspace_user_state s
                JOIN super_workspace_chats c
                  ON c.id = s.active_chat_id
                 AND c.workspace_id = s.active_workspace_id
                 AND c.user_id = s.user_id
                WHERE s.user_id = ? AND s.active_workspace_id = ?
                LIMIT 1
                """,
                (row["user_id"], row["workspace_id"]),
            ).scalar()
            if not chat_id:
                chat_id = connection.exec_driver_sql(
                    """
                    SELECT id
                    FROM super_workspace_chats
                    WHERE user_id = ? AND workspace_id = ?
                    ORDER BY created_at ASC, id ASC
                    LIMIT 1
                    """,
                    (row["user_id"], row["workspace_id"]),
                ).scalar()
            if not chat_id:
                continue
            provider, viewer_session_id = self._parse_session_ref(str(row["session_ref"] or ""), str(row["provider"] or "codex"))
            connection.exec_driver_sql(
                """
                INSERT INTO super_workspace_chat_role_sessions (
                  id, workspace_id, chat_id, user_id, role_id, provider,
                  session_ref, viewer_session_id, provider_session_id, cwd, model, session_policy_snapshot,
                  model_context_window, total_tokens, context_used_percent, usage_updated_at,
                  last_driver_run_id, last_message_id, rotation_count, last_rotation_reason,
                  created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, NULL, NULL, NULL, NULL, '', '', 0, ?, ?, ?)
                ON CONFLICT(user_id, workspace_id, chat_id, role_id, provider) DO UPDATE SET
                  session_ref = excluded.session_ref,
                  viewer_session_id = excluded.viewer_session_id,
                  cwd = excluded.cwd,
                  model = excluded.model,
                  session_policy_snapshot = excluded.session_policy_snapshot,
                  last_rotation_reason = excluded.last_rotation_reason,
                  updated_at = excluded.updated_at
                """,
                (
                    uuid.uuid4().hex,
                    row["workspace_id"],
                    chat_id,
                    row["user_id"],
                    row["id"],
                    provider,
                    row["session_ref"],
                    viewer_session_id,
                    row["cwd"] or "",
                    row["model"],
                    row["session_policy"] or "reuse",
                    "role_session_ref_migration",
                    now,
                    now,
                ),
            )

    def _migrate_invalid_super_chat_ids(self, connection) -> None:
        bad_chat_ids = connection.exec_driver_sql(
            """
            SELECT id
            FROM super_workspace_chats
            WHERE id NOT GLOB '[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]'
            """
        ).scalars().all()
        if not bad_chat_ids:
            return

        existing_ids = {
            str(row[1])
            for row in connection.exec_driver_sql("SELECT id FROM super_workspace_chats").all()
        }

        for bad_chat_id in bad_chat_ids:
            if CHAT_ID_RE.fullmatch(bad_chat_id):
                continue
            new_chat_id = uuid.uuid4().hex[:12]
            while new_chat_id in existing_ids:
                new_chat_id = uuid.uuid4().hex[:12]
            existing_ids.add(new_chat_id)

            tables = [
                ("super_workspace_messages", "conversation_id"),
                ("super_workspace_chat_role_sessions", "chat_id"),
                ("super_workspace_chat_pins", "chat_id"),
                ("super_workspace_user_state", "active_chat_id"),
            ]
            for table, column in tables:
                connection.exec_driver_sql(
                    f"UPDATE {table} SET {column} = :new_chat_id WHERE {column} = :old_chat_id",
                    {"new_chat_id": new_chat_id, "old_chat_id": bad_chat_id},
                )
            connection.exec_driver_sql(
                "UPDATE super_workspace_chats SET id = :new_chat_id WHERE id = :old_chat_id",
                {"new_chat_id": new_chat_id, "old_chat_id": bad_chat_id},
            )

    def ensure_default_workspace(self, user_id: str | None) -> SuperWorkspaceRow:
        normalized_user = normalize_user_id(user_id)
        workspace_id = self.default_workspace_id(normalized_user)
        now = time.time()
        with self.session_scope() as db:
            statement = sqlite_insert(SuperWorkspaceRow).values(
                id=workspace_id,
                user_id=normalized_user,
                name=DEFAULT_SUPER_WORKSPACE_NAME,
                common_prompt="",
                created_at=now,
                updated_at=now,
            )
            db.execute(statement.on_conflict_do_nothing(index_elements=["user_id", "id"]))
            row = db.scalar(
                select(SuperWorkspaceRow).where(
                    SuperWorkspaceRow.id == workspace_id,
                    SuperWorkspaceRow.user_id == normalized_user,
                )
            )
            state = db.scalar(select(SuperWorkspaceUserStateRow).where(SuperWorkspaceUserStateRow.user_id == normalized_user))
            if state is None:
                db.add(
                    SuperWorkspaceUserStateRow(
                        user_id=normalized_user,
                        active_workspace_id=workspace_id,
                        active_chat_id="",
                        updated_at=now,
                    )
                )
            if row is not None:
                return row
        return SuperWorkspaceRow(
            id=workspace_id,
            user_id=normalized_user,
            name=DEFAULT_SUPER_WORKSPACE_NAME,
            common_prompt="",
            created_at=now,
            updated_at=now,
        )

    def ensure_workspace(self, user_id: str | None, workspace_id: str, name: str) -> SuperWorkspaceRow:
        normalized_user = normalize_user_id(user_id)
        cleaned_id = workspace_id.strip()
        if not cleaned_id:
            raise ValueError("workspace_id is required")
        now = time.time()
        with self.session_scope() as db:
            statement = sqlite_insert(SuperWorkspaceRow).values(
                id=cleaned_id,
                user_id=normalized_user,
                name=name,
                common_prompt="",
                created_at=now,
                updated_at=now,
            )
            db.execute(
                statement.on_conflict_do_update(
                    index_elements=["user_id", "id"],
                    set_={"name": name, "updated_at": now},
                )
            )
            row = db.scalar(
                select(SuperWorkspaceRow).where(
                    SuperWorkspaceRow.id == cleaned_id,
                    SuperWorkspaceRow.user_id == normalized_user,
                )
            )
            if row is not None:
                return row
        return SuperWorkspaceRow(id=cleaned_id, user_id=normalized_user, name=name, common_prompt="", created_at=now, updated_at=now)

    @staticmethod
    def default_workspace_id(user_id: str | None) -> str:
        return f"{normalize_user_id(user_id)}:default"

    def list_super_workspaces(self, user_id: str | None) -> SuperWorkspaceList:
        workspace = self.active_super_workspace(user_id)
        with self.read_session() as db:
            rows = list(
                db.scalars(
                    select(SuperWorkspaceRow)
                    .where(SuperWorkspaceRow.user_id == workspace.user_id)
                    .order_by(SuperWorkspaceRow.created_at.asc(), SuperWorkspaceRow.id.asc())
                ).all()
            )
        return SuperWorkspaceList(
            active_workspace_id=workspace.id,
            workspaces=[
                SuperWorkspaceSummary(
                    id=str(row.id),
                    name=str(row.name),
                    created_at=float(row.created_at),
                    updated_at=float(row.updated_at),
                )
                for row in rows
            ],
        )

    def active_super_chat(self, user_id: str | None, workspace_id: str | None = None) -> SuperWorkspaceChatRow:
        workspace = self._workspace_for_run(normalize_user_id(user_id), workspace_id)
        normalized_user = workspace.user_id
        with self.read_session() as db:
            state = db.scalar(select(SuperWorkspaceUserStateRow).where(SuperWorkspaceUserStateRow.user_id == normalized_user))
            active_chat_id = state.active_chat_id.strip() if state is not None and state.active_workspace_id == workspace.id else ""
            if not active_chat_id:
                raise KeyError("No active chat")
            row = db.scalar(
                select(SuperWorkspaceChatRow).where(
                    SuperWorkspaceChatRow.id == active_chat_id,
                    SuperWorkspaceChatRow.workspace_id == workspace.id,
                    SuperWorkspaceChatRow.user_id == normalized_user,
                )
            )
            if row is None:
                raise KeyError(active_chat_id)
            return row

    def _chat_for_super_run(self, user_id: str, workspace_id: str, chat_id: str | None) -> SuperWorkspaceChatRow:
        if chat_id:
            return self._chat_for_run(user_id, workspace_id, chat_id)
        return self.active_super_chat(user_id, workspace_id)

    def list_super_chats(self, user_id: str | None, workspace_id: str | None = None) -> SuperChatList:
        workspace = self._workspace_for_run(normalize_user_id(user_id), workspace_id)
        with self.read_session() as db:
            state = db.scalar(select(SuperWorkspaceUserStateRow).where(SuperWorkspaceUserStateRow.user_id == workspace.user_id))
            active_chat_id = state.active_chat_id.strip() if state is not None and state.active_workspace_id == workspace.id else ""
            role_names = dict(
                db.execute(
                    select(SuperWorkspaceRoleRow.id, SuperWorkspaceRoleRow.name).where(
                        SuperWorkspaceRoleRow.workspace_id == workspace.id,
                        SuperWorkspaceRoleRow.user_id == workspace.user_id,
                    )
                ).all()
            )
            rows = list(
                db.scalars(
                    select(SuperWorkspaceChatRow)
                    .where(
                        SuperWorkspaceChatRow.workspace_id == workspace.id,
                        SuperWorkspaceChatRow.user_id == workspace.user_id,
                    )
                    .order_by(SuperWorkspaceChatRow.created_at.asc(), SuperWorkspaceChatRow.id.asc())
                ).all()
            )
            pinned_chat_ids = {
                str(chat_id)
                for chat_id in db.scalars(
                    select(SuperWorkspaceChatPinRow.chat_id).where(
                        SuperWorkspaceChatPinRow.workspace_id == workspace.id,
                        SuperWorkspaceChatPinRow.user_id == workspace.user_id,
                    )
                ).all()
            }
        if active_chat_id and all(str(row.id) != active_chat_id for row in rows):
            active_chat_id = ""
        return SuperChatList(
            active_chat_id=active_chat_id,
            chats=[
                SuperChatSummary(
                    id=str(row.id),
                    workspace_id=str(row.workspace_id),
                    name=self._display_chat_name(row, role_names),
                    type=str(row.type or "group"),
                    pinned=str(row.id) in pinned_chat_ids,
                    cwd=str(row.cwd or ""),
                    common_prompt=str(row.common_prompt or ""),
                    member_role_ids=[value for value in self._parse_json(row.member_role_ids_json, []) if isinstance(value, str)],
                    created_at=float(row.created_at),
                    updated_at=float(row.updated_at),
                )
                for row in rows
            ],
        )

    def create_super_chat(
        self,
        user_id: str | None,
        *,
        name: str,
        chat_type: str = "group",
        pinned: bool = False,
        cwd: str = "",
        common_prompt: str = "",
        member_role_ids: list[str] | None = None,
        workspace_id: str | None = None,
    ) -> SuperChatList:
        workspace = self._workspace_for_run(normalize_user_id(user_id), workspace_id)
        normalized_name, normalized_type, normalized_role_ids = self._normalize_chat_values(
            workspace.user_id,
            workspace.id,
            name=name,
            chat_type=chat_type,
            member_role_ids=member_role_ids or [],
        )
        now = time.time()
        chat_id = uuid.uuid4().hex[:12]
        with self.session_scope() as db:
            db.add(
                SuperWorkspaceChatRow(
                    id=chat_id,
                    workspace_id=workspace.id,
                    user_id=workspace.user_id,
                    name=normalized_name,
                    type=normalized_type,
                    cwd=normalize_relative_cwd(cwd),
                    common_prompt=common_prompt.strip(),
                    member_role_ids_json=self._json(normalized_role_ids),
                    created_at=now,
                    updated_at=now,
                )
            )
            if pinned:
                db.add(
                    SuperWorkspaceChatPinRow(
                        user_id=workspace.user_id,
                        workspace_id=workspace.id,
                        chat_id=chat_id,
                        created_at=now,
                        updated_at=now,
                    )
                )
            statement = sqlite_insert(SuperWorkspaceUserStateRow).values(
                user_id=workspace.user_id,
                active_workspace_id=workspace.id,
                active_chat_id=chat_id,
                updated_at=now,
            )
            db.execute(
                statement.on_conflict_do_update(
                    index_elements=["user_id"],
                    set_={"active_workspace_id": workspace.id, "active_chat_id": chat_id, "updated_at": now},
                )
            )
        return self.list_super_chats(workspace.user_id, workspace.id)

    def update_super_chat(self, user_id: str | None, chat_id: str, values: dict[str, Any], workspace_id: str | None = None) -> SuperChatList:
        workspace = self._workspace_for_run(normalize_user_id(user_id), workspace_id)
        with self.session_scope() as db:
            row = db.scalar(
                select(SuperWorkspaceChatRow).where(
                    SuperWorkspaceChatRow.id == chat_id,
                    SuperWorkspaceChatRow.workspace_id == workspace.id,
                    SuperWorkspaceChatRow.user_id == workspace.user_id,
                )
            )
            if row is None:
                raise KeyError(chat_id)
            if values:
                next_name = values["name"] if "name" in values else row.name
                next_type = values["type"] if "type" in values else row.type
                next_member_ids = values["member_role_ids"] if "member_role_ids" in values else self._parse_json(row.member_role_ids_json, [])
                normalized_name, normalized_type, normalized_role_ids = self._normalize_chat_values(
                    workspace.user_id,
                    workspace.id,
                    name=str(next_name or ""),
                    chat_type=str(next_type or "group"),
                    member_role_ids=[str(value) for value in next_member_ids if isinstance(value, str)],
                )
                row.name = normalized_name
                row.type = normalized_type
                row.member_role_ids_json = self._json(normalized_role_ids)
                if "pinned" in values:
                    now = time.time()
                    if values["pinned"]:
                        statement = sqlite_insert(SuperWorkspaceChatPinRow).values(
                            user_id=workspace.user_id,
                            workspace_id=workspace.id,
                            chat_id=chat_id,
                            created_at=now,
                            updated_at=now,
                        )
                        db.execute(
                            statement.on_conflict_do_update(
                                index_elements=["user_id", "workspace_id", "chat_id"],
                                set_={"updated_at": now},
                            )
                        )
                    else:
                        db.execute(
                            delete(SuperWorkspaceChatPinRow).where(
                                SuperWorkspaceChatPinRow.user_id == workspace.user_id,
                                SuperWorkspaceChatPinRow.workspace_id == workspace.id,
                                SuperWorkspaceChatPinRow.chat_id == chat_id,
                            )
                        )
                if "cwd" in values:
                    row.cwd = normalize_relative_cwd(values["cwd"])
                if "common_prompt" in values:
                    row.common_prompt = str(values["common_prompt"] or "").strip()
                row.updated_at = time.time()
        return self.list_super_chats(workspace.user_id, workspace.id)

    def _normalize_chat_member_role_ids(self, user_id: str, workspace_id: str, role_ids: list[str]) -> list[str]:
        requested = [str(role_id).strip() for role_id in role_ids if str(role_id).strip()]
        if not requested:
            return []
        with self.read_session() as db:
            existing = {
                str(role_id)
                for role_id in db.scalars(
                    select(SuperWorkspaceRoleRow.id).where(
                        SuperWorkspaceRoleRow.user_id == user_id,
                        SuperWorkspaceRoleRow.workspace_id == workspace_id,
                        SuperWorkspaceRoleRow.id.in_(requested),
                    )
                ).all()
            }
        return [role_id for role_id in requested if role_id in existing]

    def _normalize_chat_values(
        self,
        user_id: str,
        workspace_id: str,
        *,
        name: str,
        chat_type: str,
        member_role_ids: list[str],
    ) -> tuple[str, str, list[str]]:
        normalized_type = chat_type if chat_type in {"group", "direct"} else "group"
        normalized_role_ids = self._normalize_chat_member_role_ids(user_id, workspace_id, member_role_ids)
        if normalized_type == "direct":
            if len(normalized_role_ids) != 1:
                raise ValueError("Direct chats require exactly one role")
            role_name = self._role_name(user_id, workspace_id, normalized_role_ids[0])
            return role_name[:120] or "Direct", normalized_type, normalized_role_ids
        return name.strip()[:120] or "New Chat", normalized_type, normalized_role_ids

    def _role_name(self, user_id: str, workspace_id: str, role_id: str) -> str:
        with self.read_session() as db:
            name = db.scalar(
                select(SuperWorkspaceRoleRow.name).where(
                    SuperWorkspaceRoleRow.user_id == user_id,
                    SuperWorkspaceRoleRow.workspace_id == workspace_id,
                    SuperWorkspaceRoleRow.id == role_id,
                )
            )
        if name is None:
            raise ValueError("Direct chat role not found")
        return str(name)

    def _display_chat_name(self, row: SuperWorkspaceChatRow, role_names: dict[str, str]) -> str:
        member_ids = [value for value in self._parse_json(row.member_role_ids_json, []) if isinstance(value, str)]
        if str(row.type or "group") == "direct" and len(member_ids) == 1:
            return str(role_names.get(member_ids[0]) or row.name)
        return str(row.name)

    def delete_super_chat(self, user_id: str | None, chat_id: str, workspace_id: str | None = None) -> SuperChatList:
        workspace = self._workspace_for_run(normalize_user_id(user_id), workspace_id)
        now = time.time()
        with self.session_scope() as db:
            db.execute(
                delete(SuperWorkspaceChatPinRow).where(
                    SuperWorkspaceChatPinRow.chat_id == chat_id,
                    SuperWorkspaceChatPinRow.workspace_id == workspace.id,
                    SuperWorkspaceChatPinRow.user_id == workspace.user_id,
                )
            )
            result = db.execute(
                delete(SuperWorkspaceChatRow).where(
                    SuperWorkspaceChatRow.id == chat_id,
                    SuperWorkspaceChatRow.workspace_id == workspace.id,
                    SuperWorkspaceChatRow.user_id == workspace.user_id,
                )
            )
            if result.rowcount == 0:
                raise KeyError(chat_id)
            state = db.scalar(select(SuperWorkspaceUserStateRow).where(SuperWorkspaceUserStateRow.user_id == workspace.user_id))
            if state is not None and state.active_chat_id == chat_id:
                state.active_chat_id = ""
                state.updated_at = now
        return self.list_super_chats(workspace.user_id, workspace.id)

    def activate_super_chat(self, user_id: str | None, chat_id: str, workspace_id: str | None = None) -> SuperChatList:
        workspace = self._workspace_for_run(normalize_user_id(user_id), workspace_id)
        now = time.time()
        with self.session_scope() as db:
            row = db.scalar(
                select(SuperWorkspaceChatRow).where(
                    SuperWorkspaceChatRow.id == chat_id,
                    SuperWorkspaceChatRow.workspace_id == workspace.id,
                    SuperWorkspaceChatRow.user_id == workspace.user_id,
                )
            )
            if row is None:
                raise KeyError(chat_id)
            statement = sqlite_insert(SuperWorkspaceUserStateRow).values(
                user_id=workspace.user_id,
                active_workspace_id=workspace.id,
                active_chat_id=chat_id,
                updated_at=now,
            )
            db.execute(
                statement.on_conflict_do_update(
                    index_elements=["user_id"],
                    set_={"active_workspace_id": workspace.id, "active_chat_id": chat_id, "updated_at": now},
                )
            )
        return self.list_super_chats(workspace.user_id, workspace.id)

    def active_super_workspace(self, user_id: str | None) -> SuperWorkspaceRow:
        default = self.ensure_default_workspace(user_id)
        normalized_user = default.user_id
        now = time.time()
        with self.session_scope() as db:
            state = db.scalar(select(SuperWorkspaceUserStateRow).where(SuperWorkspaceUserStateRow.user_id == normalized_user))
            active_id = state.active_workspace_id if state is not None else ""
            row = None
            if active_id:
                row = db.scalar(
                    select(SuperWorkspaceRow).where(
                        SuperWorkspaceRow.id == active_id,
                        SuperWorkspaceRow.user_id == normalized_user,
                    )
                )
            if row is None:
                row = db.scalar(
                    select(SuperWorkspaceRow)
                    .where(SuperWorkspaceRow.user_id == normalized_user)
                    .order_by(SuperWorkspaceRow.created_at.asc(), SuperWorkspaceRow.id.asc())
                    .limit(1)
                )
            if row is None:
                row = default
            if state is None:
                db.add(
                    SuperWorkspaceUserStateRow(
                        user_id=normalized_user,
                        active_workspace_id=row.id,
                        active_chat_id="",
                        updated_at=now,
                    )
                )
            elif state.active_workspace_id != row.id:
                state.active_workspace_id = row.id
                state.active_chat_id = ""
                state.updated_at = now
            return row

    def activate_super_workspace(self, user_id: str | None, workspace_id: str) -> SuperWorkspaceList:
        normalized_user = normalize_user_id(user_id)
        self.ensure_default_workspace(normalized_user)
        now = time.time()
        with self.session_scope() as db:
            row = db.scalar(
                select(SuperWorkspaceRow).where(
                    SuperWorkspaceRow.id == workspace_id,
                    SuperWorkspaceRow.user_id == normalized_user,
                )
            )
            if row is None:
                raise KeyError(workspace_id)
            statement = sqlite_insert(SuperWorkspaceUserStateRow).values(
                user_id=normalized_user,
                active_workspace_id=workspace_id,
                active_chat_id="",
                updated_at=now,
            )
            db.execute(
                statement.on_conflict_do_update(
                    index_elements=["user_id"],
                    set_={"active_workspace_id": workspace_id, "active_chat_id": "", "updated_at": now},
                )
            )
        return self.list_super_workspaces(normalized_user)

    def super_workspace_data(self, user_id: str | None) -> tuple[SuperWorkspaceRow, list[SuperWorkspaceRoleRow]]:
        workspace = self.active_super_workspace(user_id)
        with self.read_session() as db:
            row = db.scalar(
                select(SuperWorkspaceRow).where(
                    SuperWorkspaceRow.id == workspace.id,
                    SuperWorkspaceRow.user_id == workspace.user_id,
                )
            )
            if row is None:
                raise KeyError(workspace.id)
            roles = list(
                db.scalars(
                    select(SuperWorkspaceRoleRow)
                    .where(
                        SuperWorkspaceRoleRow.workspace_id == workspace.id,
                        SuperWorkspaceRoleRow.user_id == workspace.user_id,
                    )
                    .order_by(SuperWorkspaceRoleRow.created_at.asc(), SuperWorkspaceRoleRow.id.asc())
                ).all()
            )
            return row, roles

    def update_super_workspace_common_prompt(self, user_id: str | None, common_prompt: str) -> None:
        workspace = self.active_super_workspace(user_id)
        now = time.time()
        with self.session_scope() as db:
            db.execute(
                update(SuperWorkspaceRow)
                .where(SuperWorkspaceRow.id == workspace.id, SuperWorkspaceRow.user_id == workspace.user_id)
                .values(common_prompt=common_prompt, updated_at=now)
            )

    def create_super_workspace_role(
        self,
        user_id: str | None,
        *,
        name: str,
        description: str = "",
        provider: str = "codex",
        cwd: str = "",
        model: str | None = None,
        session_policy: str = "reuse",
    ) -> None:
        workspace = self.active_super_workspace(user_id)
        now = time.time()
        normalized_policy = session_policy if session_policy in ROLE_SESSION_POLICIES else "reuse"
        with self.session_scope() as db:
            db.add(
                SuperWorkspaceRoleRow(
                    id=uuid.uuid4().hex[:12],
                    workspace_id=workspace.id,
                    user_id=workspace.user_id,
                    name=name,
                    description=description,
                    provider=provider,
                    cwd=normalize_relative_cwd(cwd),
                    model=model,
                    session_policy=normalized_policy,
                    created_at=now,
                    updated_at=now,
                )
            )

    def update_super_workspace_role(self, user_id: str | None, role_id: str, values: dict[str, Any]) -> None:
        workspace = self.active_super_workspace(user_id)
        cleaned = {key: value for key, value in values.items() if value is not None}
        if not cleaned:
            return
        if "session_policy" in cleaned and cleaned["session_policy"] not in ROLE_SESSION_POLICIES:
            cleaned["session_policy"] = "reuse"
        if "cwd" in cleaned:
            cleaned["cwd"] = normalize_relative_cwd(cleaned["cwd"])
        cleaned["updated_at"] = time.time()
        with self.session_scope() as db:
            db.execute(
                update(SuperWorkspaceRoleRow)
                .where(
                    SuperWorkspaceRoleRow.id == role_id,
                    SuperWorkspaceRoleRow.workspace_id == workspace.id,
                    SuperWorkspaceRoleRow.user_id == workspace.user_id,
                )
                .values(**cleaned)
            )

    def delete_super_workspace_role(self, user_id: str | None, role_id: str) -> None:
        workspace = self.active_super_workspace(user_id)
        with self.session_scope() as db:
            db.execute(
                delete(SuperWorkspaceRoleRow).where(
                    SuperWorkspaceRoleRow.id == role_id,
                    SuperWorkspaceRoleRow.workspace_id == workspace.id,
                    SuperWorkspaceRoleRow.user_id == workspace.user_id,
                )
            )
            db.execute(
                delete(SuperWorkspaceChatRoleSessionRow).where(
                    SuperWorkspaceChatRoleSessionRow.role_id == role_id,
                    SuperWorkspaceChatRoleSessionRow.workspace_id == workspace.id,
                    SuperWorkspaceChatRoleSessionRow.user_id == workspace.user_id,
                )
            )

    def _chat_role_session_from_row(self, row: SuperWorkspaceChatRoleSessionRow) -> SuperChatRoleSessionState:
        return SuperChatRoleSessionState(
            id=str(row.id),
            workspace_id=str(row.workspace_id),
            chat_id=str(row.chat_id),
            user_id=str(row.user_id),
            role_id=str(row.role_id),
            provider=str(row.provider),
            session_ref=str(row.session_ref or ""),
            viewer_session_id=str(row.viewer_session_id or ""),
            provider_session_id=row.provider_session_id if isinstance(row.provider_session_id, str) else None,
            cwd=str(row.cwd or ""),
            model=row.model if isinstance(row.model, str) else None,
            session_policy_snapshot=str(row.session_policy_snapshot or "reuse"),
            model_context_window=int(row.model_context_window) if isinstance(row.model_context_window, int) else None,
            total_tokens=int(row.total_tokens) if isinstance(row.total_tokens, int) else None,
            context_used_percent=float(row.context_used_percent) if isinstance(row.context_used_percent, (int, float)) else None,
            usage_updated_at=float(row.usage_updated_at) if isinstance(row.usage_updated_at, (int, float)) else None,
            last_driver_run_id=str(row.last_driver_run_id or ""),
            last_message_id=str(row.last_message_id or ""),
            rotation_count=int(row.rotation_count or 0),
            last_rotation_reason=str(row.last_rotation_reason or ""),
            created_at=float(row.created_at),
            updated_at=float(row.updated_at),
        )

    def get_chat_role_session(
        self,
        user_id: str | None,
        workspace_id: str,
        chat_id: str,
        role_id: str,
        provider: str,
    ) -> SuperChatRoleSessionState | None:
        normalized_user = normalize_user_id(user_id)
        with self.read_session() as db:
            row = db.scalar(
                select(SuperWorkspaceChatRoleSessionRow).where(
                    SuperWorkspaceChatRoleSessionRow.user_id == normalized_user,
                    SuperWorkspaceChatRoleSessionRow.workspace_id == workspace_id,
                    SuperWorkspaceChatRoleSessionRow.chat_id == chat_id,
                    SuperWorkspaceChatRoleSessionRow.role_id == role_id,
                    SuperWorkspaceChatRoleSessionRow.provider == provider,
                )
            )
            return self._chat_role_session_from_row(row) if row is not None else None

    def upsert_chat_role_session(
        self,
        user_id: str | None,
        *,
        workspace_id: str,
        chat_id: str,
        role_id: str,
        provider: str,
        session_ref: str,
        cwd: str,
        model: str | None,
        session_policy: str,
        driver_run_id: str = "",
        message_id: str = "",
        rotation_reason: str = "",
        provider_session_id: str | None = None,
        model_context_window: int | None = None,
        total_tokens: int | None = None,
        context_used_percent: float | None = None,
    ) -> SuperChatRoleSessionState:
        normalized_user = normalize_user_id(user_id)
        parsed_provider, viewer_session_id = self._parse_session_ref(session_ref, provider)
        now = time.time()
        usage_updated_at = now if any(value is not None for value in (model_context_window, total_tokens, context_used_percent)) else None
        with self.session_scope() as db:
            existing = db.scalar(
                select(SuperWorkspaceChatRoleSessionRow).where(
                    SuperWorkspaceChatRoleSessionRow.user_id == normalized_user,
                    SuperWorkspaceChatRoleSessionRow.workspace_id == workspace_id,
                    SuperWorkspaceChatRoleSessionRow.chat_id == chat_id,
                    SuperWorkspaceChatRoleSessionRow.role_id == role_id,
                    SuperWorkspaceChatRoleSessionRow.provider == parsed_provider,
                )
            )
            if existing is None:
                row = SuperWorkspaceChatRoleSessionRow(
                    id=uuid.uuid4().hex,
                    workspace_id=workspace_id,
                    chat_id=chat_id,
                    user_id=normalized_user,
                    role_id=role_id,
                    provider=parsed_provider,
                    session_ref=session_ref,
                    viewer_session_id=viewer_session_id,
                    provider_session_id=provider_session_id,
                    cwd=cwd,
                    model=model,
                    session_policy_snapshot=session_policy if session_policy in ROLE_SESSION_POLICIES else "reuse",
                    model_context_window=model_context_window,
                    total_tokens=total_tokens,
                    context_used_percent=context_used_percent,
                    usage_updated_at=usage_updated_at,
                    last_driver_run_id=driver_run_id,
                    last_message_id=message_id,
                    rotation_count=1 if rotation_reason else 0,
                    last_rotation_reason=rotation_reason,
                    created_at=now,
                    updated_at=now,
                )
                db.add(row)
                db.flush()
                return self._chat_role_session_from_row(row)
            is_replacement = bool(session_ref) and session_ref != str(existing.session_ref or "")
            existing.session_ref = session_ref
            existing.viewer_session_id = viewer_session_id
            existing.provider_session_id = provider_session_id
            existing.cwd = cwd
            existing.model = model
            existing.session_policy_snapshot = session_policy if session_policy in ROLE_SESSION_POLICIES else "reuse"
            if model_context_window is not None:
                existing.model_context_window = model_context_window
            if total_tokens is not None:
                existing.total_tokens = total_tokens
            if context_used_percent is not None:
                existing.context_used_percent = context_used_percent
            if usage_updated_at is not None:
                existing.usage_updated_at = usage_updated_at
            if driver_run_id:
                existing.last_driver_run_id = driver_run_id
            if message_id:
                existing.last_message_id = message_id
            if rotation_reason:
                existing.last_rotation_reason = rotation_reason
                if is_replacement:
                    existing.rotation_count = int(existing.rotation_count or 0) + 1
            existing.updated_at = now
            db.flush()
            return self._chat_role_session_from_row(existing)

    def create_super_run(
        self,
        user_id: str | None,
        message: str,
        status: str,
        role_ids: list[str] | None = None,
        citation_ids: list[str] | None = None,
        rationale: str = "",
        raw: dict[str, Any] | None = None,
        parent_message_id: str | None = None,
        sender_role_id: str | None = None,
        workspace_id: str | None = None,
        chat_id: str | None = None,
    ) -> SuperHistoryRun:
        normalized_user = normalize_user_id(user_id)
        workspace = self._workspace_for_run(normalized_user, workspace_id)
        chat = self._chat_for_super_run(normalized_user, workspace.id, chat_id)
        now = time.time()
        message_id = uuid.uuid4().hex
        normalized_citation_ids = self._normalize_citation_ids(citation_ids or [])
        raw_message = raw or {
            "type": "super_workspace_query_message",
            "message_id": message_id,
            "query": message,
            "role_ids": role_ids or [],
            "citation_ids": normalized_citation_ids,
            "parent_message_id": parent_message_id,
            "sender_role_id": sender_role_id,
            "chat_id": chat.id,
        }
        with self.session_scope() as db:
            self._validate_citation_ids(db, normalized_user, normalized_citation_ids)
            self._insert_message(
                db,
                {
                    "id": message_id,
                    "workspace_id": workspace.id,
                    "user_id": normalized_user,
                    "conversation_id": chat.id,
                    "parent_message_id": parent_message_id,
                    "sender_role_id": sender_role_id,
                    "recipient_role_id": None,
                    "role_id": sender_role_id or "user",
                    "query_message_id": None,
                    "driver_run_id": None,
                    "provider": "",
                    "viewer_session_id": None,
                    "provider_session_id": None,
                    "event_index": 0,
                    "received_at": now,
                    "source_path": None,
                    "source_event_id": f"message:{message_id}:query",
                    "source_line": None,
                    "role": "user" if not sender_role_id else "assistant",
                    "event_type": "message:query",
                    "text": "",
                    "query": message,
                    "status": status,
                    "rationale": rationale,
                    "error": "",
                    "requested_role_ids_json": self._json(role_ids or []),
                    "selected_role_ids_json": self._json(role_ids or []),
                    "patch_text": None,
                    "raw_json": self._json(raw_message),
                    "occurred_at": now,
                    "ingested_at": now,
                },
            )
            self._replace_citations(db, message_id, normalized_citation_ids, now)
        retain_visible_message_background(
            user_id=normalized_user,
            workspace_id=workspace.id,
            chat_id=chat.id,
            message_id=message_id,
            role="user" if not sender_role_id else "assistant",
            text=message,
            occurred_at=now,
            provider="super_workspace",
            event_type="message:query",
            role_id=sender_role_id or "user",
            sender_role_id=sender_role_id,
        )
        return self.get_super_run(message_id, normalized_user)

    def _chat_for_run(self, user_id: str, workspace_id: str, chat_id: str) -> SuperWorkspaceChatRow:
        with self.read_session() as db:
            row = db.scalar(
                select(SuperWorkspaceChatRow).where(
                    SuperWorkspaceChatRow.id == chat_id,
                    SuperWorkspaceChatRow.workspace_id == workspace_id,
                    SuperWorkspaceChatRow.user_id == user_id,
                )
            )
            if row is None:
                raise KeyError(chat_id)
            return row

    def _workspace_for_run(self, user_id: str, workspace_id: str | None = None) -> SuperWorkspaceRow:
        if workspace_id:
            with self.read_session() as db:
                row = db.scalar(
                    select(SuperWorkspaceRow).where(
                        SuperWorkspaceRow.id == workspace_id,
                        SuperWorkspaceRow.user_id == user_id,
                    )
                )
                if row is None:
                    raise KeyError(workspace_id)
                return row
        return self.active_super_workspace(user_id)

    def update_super_run(
        self,
        run_id: str,
        user_id: str | None,
        *,
        status: str | None = None,
        role_ids: list[str] | None = None,
        rationale: str | None = None,
        error: str | None = None,
    ) -> SuperHistoryRun:
        normalized_user = normalize_user_id(user_id)
        now = time.time()
        values: dict[str, Any] = {"ingested_at": now}
        if status is not None:
            values["status"] = status
        if role_ids is not None:
            values["selected_role_ids_json"] = self._json(role_ids)
        if rationale is not None:
            values["rationale"] = rationale
        if error is not None:
            values["error"] = error
        with self.session_scope() as db:
            result = db.execute(
                update(SuperWorkspaceMessageRow)
                .where(
                    SuperWorkspaceMessageRow.id == run_id,
                    SuperWorkspaceMessageRow.user_id == normalized_user,
                    SuperWorkspaceMessageRow.query.is_not(None),
                    SuperWorkspaceMessageRow.query != "",
                )
                .values(**values)
            )
            if result.rowcount == 0:
                raise KeyError(run_id)
        return self.get_super_run(run_id, normalized_user)

    def record_super_target(self, user_id: str | None, run_id: str, request: SuperDriverRunCreate) -> SuperHistoryRun:
        return self.create_dispatch_task(user_id, run_id, request)

    def create_dispatch_task(self, user_id: str | None, query_message_id: str, request: SuperDriverRunCreate) -> SuperHistoryRun:
        normalized_user = normalize_user_id(user_id)
        workspace = self._workspace_for_run(normalized_user, request.workspace_id)
        provider, viewer_session_id = self._parse_session_ref(request.session_ref, request.provider)
        provider_session_id = self._provider_session_id(provider, viewer_session_id)
        now = time.time()
        start = self._latest_session_message(provider, viewer_session_id) if viewer_session_id else None
        with self.read_session() as db:
            query = db.scalar(
                select(SuperWorkspaceMessageRow).where(
                    SuperWorkspaceMessageRow.id == query_message_id,
                    SuperWorkspaceMessageRow.workspace_id == workspace.id,
                    SuperWorkspaceMessageRow.user_id == normalized_user,
                    SuperWorkspaceMessageRow.query.is_not(None),
                    SuperWorkspaceMessageRow.query != "",
                )
            )
            if query is None:
                raise KeyError(query_message_id)
            existing = db.scalar(
                select(SuperWorkspaceDriverRunRow.id).where(
                    SuperWorkspaceDriverRunRow.query_message_id == query_message_id,
                    SuperWorkspaceDriverRunRow.workspace_id == workspace.id,
                    SuperWorkspaceDriverRunRow.role_id == request.role_id,
                    SuperWorkspaceDriverRunRow.provider == provider,
                )
            )
            sender_role_id = request.sender_role_id or query.sender_role_id
            chat_id = request.chat_id or str(query.conversation_id or "")
        if existing is None:
            with self.session_scope() as db:
                db.add(
                    SuperWorkspaceDriverRunRow(
                        id=uuid.uuid4().hex,
                        workspace_id=workspace.id,
                        query_message_id=query_message_id,
                        user_id=normalized_user,
                        role_id=request.role_id,
                        role_name=request.role_name,
                        provider=provider,
                        viewer_session_id=viewer_session_id,
                        provider_session_id=provider_session_id,
                        session_ref=request.session_ref,
                        agent_prompt=request.agent_prompt,
                        status="queued",
                        parent_message_id=request.parent_message_id or query_message_id,
                        sender_role_id=sender_role_id,
                        recipient_role_id=request.role_id,
                        role_snapshot_json=self._json(request.role_snapshot),
                        attempt_count=0,
                        error="",
                        start_after_occurred_at=float(start.occurred_at) if start else now,
                        created_at=now,
                        updated_at=now,
                    )
                )
        return self.get_super_run(query_message_id, normalized_user)

    def update_driver_run_status(
        self,
        driver_run_id: str,
        status: str,
        *,
        session_ref: str | None = None,
        agent_prompt: str | None = None,
        error: str | None = None,
        driver_pid: int | None = None,
        driver_state_path: str | None = None,
        next_attempt_at: float | None = None,
        provider_session_id: str | None = None,
        model_context_window: int | None = None,
        total_tokens: int | None = None,
        context_used_percent: float | None = None,
    ) -> None:
        values: dict[str, Any] = {"status": status, "updated_at": time.time()}
        if session_ref is not None:
            provider, viewer_session_id = self._parse_session_ref(session_ref, "")
            values["session_ref"] = session_ref
            values["provider"] = provider
            values["viewer_session_id"] = viewer_session_id
            values["provider_session_id"] = self._provider_session_id(provider, viewer_session_id)
        if provider_session_id is not None:
            values["provider_session_id"] = provider_session_id
        if model_context_window is not None:
            values["model_context_window"] = model_context_window
        if total_tokens is not None:
            values["total_tokens"] = total_tokens
        if context_used_percent is not None:
            values["context_used_percent"] = context_used_percent
        if agent_prompt is not None:
            values["agent_prompt"] = agent_prompt
        if error is not None:
            values["error"] = error
        if driver_pid is not None:
            values["driver_pid"] = driver_pid
        if driver_state_path is not None:
            values["driver_state_path"] = driver_state_path
        if status == "running":
            values["started_at"] = time.time()
        if status in {"completed", "failed", "cancelled"}:
            values["finished_at"] = time.time()
            values["claimed_by"] = None
            values["claim_expires_at"] = None
        if status == "queued":
            values["claimed_by"] = None
            values["claim_expires_at"] = None
            values["next_attempt_at"] = next_attempt_at
        with self.session_scope() as db:
            statement = update(SuperWorkspaceDriverRunRow).where(SuperWorkspaceDriverRunRow.id == driver_run_id)
            if status != "cancelled":
                statement = statement.where(SuperWorkspaceDriverRunRow.status != "cancelled")
            db.execute(statement.values(**values))

    def cancel_dispatch_task(self, task_id: str, user_id: str | None) -> SuperDispatchTask:
        normalized_user = normalize_user_id(user_id)
        now = time.time()
        with self.session_scope() as db:
            result = db.execute(
                update(SuperWorkspaceDriverRunRow)
                .where(
                    SuperWorkspaceDriverRunRow.id == task_id,
                    SuperWorkspaceDriverRunRow.user_id == normalized_user,
                    SuperWorkspaceDriverRunRow.status == "running",
                )
                .values(
                    status="cancelled",
                    error="",
                    claimed_by=None,
                    claim_expires_at=None,
                    finished_at=now,
                    updated_at=now,
                )
            )
            if result.rowcount == 0:
                row = db.scalar(
                    select(SuperWorkspaceDriverRunRow).where(
                        SuperWorkspaceDriverRunRow.id == task_id,
                        SuperWorkspaceDriverRunRow.user_id == normalized_user,
                    )
                )
                if row is None:
                    raise KeyError(task_id)
                raise ValueError(str(row.status))
        task = self.get_dispatch_task(task_id)
        if task is None:
            raise KeyError(task_id)
        return task

    def claim_next_dispatch_task(self, worker_id: str, lease_seconds: float = 60.0) -> SuperDispatchTask | None:
        now = time.time()
        with self.session_scope() as db:
            self._cleanup_stale_running_driver_runs(db, now)
            db.execute(
                update(SuperWorkspaceDriverRunRow)
                .where(
                    SuperWorkspaceDriverRunRow.status == "claimed",
                    SuperWorkspaceDriverRunRow.claim_expires_at.is_not(None),
                    SuperWorkspaceDriverRunRow.claim_expires_at <= now,
                )
                .values(status="queued", claimed_by=None, claim_expires_at=None, updated_at=now)
            )
        with self.read_session() as db:
            candidates = list(
                db.scalars(
                    select(SuperWorkspaceDriverRunRow)
                    .where(
                        SuperWorkspaceDriverRunRow.status == "queued",
                        or_(SuperWorkspaceDriverRunRow.next_attempt_at.is_(None), SuperWorkspaceDriverRunRow.next_attempt_at <= now),
                    )
                    .order_by(SuperWorkspaceDriverRunRow.created_at.asc(), SuperWorkspaceDriverRunRow.id.asc())
                    .limit(20)
                ).all()
            )
        for row in candidates:
            candidate_chat_id = self._query_chat_id(str(row.query_message_id))
            active_query_ids = select(SuperWorkspaceMessageRow.id).where(
                SuperWorkspaceMessageRow.workspace_id == row.workspace_id,
                SuperWorkspaceMessageRow.user_id == row.user_id,
                SuperWorkspaceMessageRow.conversation_id == candidate_chat_id,
                SuperWorkspaceMessageRow.query.is_not(None),
                SuperWorkspaceMessageRow.query != "",
            )
            active = (
                select(SuperWorkspaceDriverRunRow.id)
                .where(
                    SuperWorkspaceDriverRunRow.id != row.id,
                    SuperWorkspaceDriverRunRow.workspace_id == row.workspace_id,
                    SuperWorkspaceDriverRunRow.user_id == row.user_id,
                    SuperWorkspaceDriverRunRow.role_id == row.role_id,
                    SuperWorkspaceDriverRunRow.query_message_id.in_(active_query_ids),
                    or_(
                        SuperWorkspaceDriverRunRow.status == "running",
                        and_(
                            SuperWorkspaceDriverRunRow.status == "claimed",
                            or_(SuperWorkspaceDriverRunRow.claim_expires_at.is_(None), SuperWorkspaceDriverRunRow.claim_expires_at > now),
                        ),
                    ),
                )
                .limit(1)
                .exists()
            )
            with self.session_scope() as db:
                claimed = db.scalar(
                    update(SuperWorkspaceDriverRunRow)
                    .where(
                        SuperWorkspaceDriverRunRow.id == row.id,
                        SuperWorkspaceDriverRunRow.status == "queued",
                        ~active,
                    )
                    .values(
                        status="claimed",
                        claimed_by=worker_id,
                        claim_expires_at=now + lease_seconds,
                        attempt_count=int(row.attempt_count or 0) + 1,
                        updated_at=now,
                    )
                    .returning(SuperWorkspaceDriverRunRow)
                )
                if claimed is not None:
                    return self._dispatch_task_from_row(claimed)
        return None

    def _cleanup_stale_running_driver_runs(self, db: Session, now: float) -> None:
        cutoff = now - STALE_DRIVER_RUN_GRACE_SECONDS
        rows = list(
            db.scalars(
                select(SuperWorkspaceDriverRunRow).where(
                    SuperWorkspaceDriverRunRow.status == "running",
                    SuperWorkspaceDriverRunRow.updated_at <= cutoff,
                    or_(
                        SuperWorkspaceDriverRunRow.driver_pid.is_not(None),
                        and_(
                            SuperWorkspaceDriverRunRow.driver_state_path.is_not(None),
                            SuperWorkspaceDriverRunRow.driver_state_path != "",
                        ),
                    ),
                )
            ).all()
        )
        for row in rows:
            status = self._stale_driver_terminal_status(db, row)
            if status is None:
                continue
            values: dict[str, Any] = {
                "status": status,
                "claimed_by": None,
                "claim_expires_at": None,
                "finished_at": row.finished_at or now,
                "updated_at": now,
            }
            if status == "failed" and not row.error:
                values["error"] = "Driver process exited before dispatch status was finalized"
            db.execute(
                update(SuperWorkspaceDriverRunRow)
                .where(
                    SuperWorkspaceDriverRunRow.id == row.id,
                    SuperWorkspaceDriverRunRow.status == "running",
                )
                .values(**values)
            )

    def _stale_driver_terminal_status(self, db: Session, row: SuperWorkspaceDriverRunRow) -> str | None:
        state = self._read_driver_state(row.driver_state_path)
        state_status = str(state.get("status") or "").strip().lower()
        exit_code = state.get("exit_code")
        if state_status in {"exited", "completed", "succeeded"}:
            return "completed"
        if state_status in {"failed", "cancelled", "canceled"}:
            return "failed"
        if isinstance(exit_code, int):
            return "completed" if exit_code == 0 else "failed"
        if isinstance(row.driver_pid, int) and row.driver_pid > 0 and not self._pid_alive(row.driver_pid):
            return "completed" if self._driver_has_final_message(db, str(row.id)) else "failed"
        return None

    @staticmethod
    def _read_driver_state(path_value: str | None) -> dict[str, Any]:
        if not isinstance(path_value, str) or not path_value:
            return {}
        try:
            value = json.loads(Path(path_value).read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            return {}
        return value if isinstance(value, dict) else {}

    @staticmethod
    def _pid_alive(pid: int) -> bool:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return False
        except PermissionError:
            return True
        return True

    @staticmethod
    def _driver_has_final_message(db: Session, driver_run_id: str) -> bool:
        return bool(
            db.scalar(
                select(SuperWorkspaceMessageRow.id)
                .where(
                    SuperWorkspaceMessageRow.driver_run_id == driver_run_id,
                    SuperWorkspaceMessageRow.role == "assistant",
                    SuperWorkspaceMessageRow.event_type == "message:assistant",
                    SuperWorkspaceMessageRow.text != "",
                )
                .limit(1)
            )
        )

    def get_dispatch_task(self, task_id: str) -> SuperDispatchTask | None:
        with self.read_session() as db:
            row = db.scalar(select(SuperWorkspaceDriverRunRow).where(SuperWorkspaceDriverRunRow.id == task_id))
            return self._dispatch_task_from_row(row) if row is not None else None

    def summarize_super_run_status(self, run_id: str, user_id: str | None, fallback_error: str = "") -> SuperHistoryRun:
        run = self.get_super_run(run_id, user_id)
        statuses = [target.status for target in run.targets]
        if any(status == "failed" for status in statuses):
            return self.update_super_run(run_id, user_id, status="failed", error=fallback_error)
        if statuses and all(status in {"completed", "cancelled"} for status in statuses):
            terminal_status = "cancelled" if any(status == "cancelled" for status in statuses) else "completed"
            return self.update_super_run(run_id, user_id, status=terminal_status, error="")
        if any(status in {"claimed", "running"} for status in statuses):
            return self.update_super_run(run_id, user_id, status="running", error="")
        return self.update_super_run(run_id, user_id, status="queued", error="")

    def list_super_runs(
        self,
        user_id: str | None,
        limit: int = DEFAULT_RUN_LIMIT,
        before: float | None = None,
        after: float | None = None,
        message_limit: int = DEFAULT_MESSAGE_LIMIT,
    ) -> SuperHistoryRunsPage:
        normalized_user = normalize_user_id(user_id)
        workspace = self.active_super_workspace(normalized_user)
        bounded_limit = max(1, min(limit, 100))
        statement = (
            select(SuperWorkspaceMessageRow)
            .where(
                SuperWorkspaceMessageRow.workspace_id == workspace.id,
                SuperWorkspaceMessageRow.user_id == normalized_user,
                SuperWorkspaceMessageRow.query.is_not(None),
                SuperWorkspaceMessageRow.query != "",
            )
        )
        if after is not None:
            changed_query_driver_runs = select(SuperWorkspaceDriverRunRow.query_message_id).where(
                SuperWorkspaceDriverRunRow.user_id == normalized_user,
                SuperWorkspaceDriverRunRow.workspace_id == workspace.id,
                SuperWorkspaceDriverRunRow.updated_at > after,
            )
            changed_message_driver_runs = select(SuperWorkspaceDriverRunRow.id).where(
                SuperWorkspaceDriverRunRow.user_id == normalized_user,
                SuperWorkspaceDriverRunRow.workspace_id == workspace.id,
                SuperWorkspaceDriverRunRow.updated_at > after,
            )
            statement = statement.where(
                or_(
                    SuperWorkspaceMessageRow.ingested_at > after,
                    SuperWorkspaceMessageRow.id.in_(changed_query_driver_runs),
                    SuperWorkspaceMessageRow.driver_run_id.in_(changed_message_driver_runs),
                )
            )
        elif before is not None:
            statement = statement.where(SuperWorkspaceMessageRow.occurred_at < before)
        statement = statement.order_by(SuperWorkspaceMessageRow.occurred_at.desc(), SuperWorkspaceMessageRow.id.desc())
        if after is None:
            statement = statement.limit(bounded_limit + 1)
        with self.read_session() as db:
            rows = list(db.scalars(statement).all())
        has_more = after is None and len(rows) > bounded_limit
        if after is None:
            rows = rows[:bounded_limit]
        runs = [self._run_from_message(row, message_limit=message_limit) for row in rows]
        next_after = max((run.updated_at for run in runs), default=None)
        return SuperHistoryRunsPage(
            runs=runs,
            has_more=has_more,
            next_before=runs[-1].created_at if has_more and runs else None,
            next_after=next_after,
        )

    def list_super_display_items(
        self,
        user_id: str | None,
        limit: int = DEFAULT_RUN_LIMIT,
        before: float | None = None,
        after: float | None = None,
        chat_id: str | None = None,
    ) -> SuperDisplayItemsPage:
        normalized_user = normalize_user_id(user_id)
        workspace = self.active_super_workspace(normalized_user)
        try:
            chat = self.active_super_chat(normalized_user, workspace.id) if not chat_id else self._chat_for_run(normalized_user, workspace.id, chat_id)
        except KeyError:
            return SuperDisplayItemsPage(items=[], has_more=False, next_before=None, next_after=after)
        bounded_limit = max(1, min(limit, 100))
        query_condition = and_(
            SuperWorkspaceMessageRow.workspace_id == workspace.id,
            SuperWorkspaceMessageRow.conversation_id == chat.id,
            SuperWorkspaceMessageRow.query.is_not(None),
            SuperWorkspaceMessageRow.query != "",
        )
        assistant_condition = and_(
            SuperWorkspaceMessageRow.workspace_id == workspace.id,
            SuperWorkspaceMessageRow.conversation_id == chat.id,
            SuperWorkspaceMessageRow.role == "assistant",
            SuperWorkspaceMessageRow.event_type == "message:assistant",
            SuperWorkspaceMessageRow.text != "",
            ~self._agent_message_event_condition(),
        )
        statement = select(SuperWorkspaceMessageRow).where(
            SuperWorkspaceMessageRow.user_id == normalized_user,
            or_(query_condition, assistant_condition),
        )
        if after is not None:
            changed_query_driver_runs = select(SuperWorkspaceDriverRunRow.query_message_id).where(
                SuperWorkspaceDriverRunRow.user_id == normalized_user,
                SuperWorkspaceDriverRunRow.workspace_id == workspace.id,
                SuperWorkspaceDriverRunRow.updated_at > after,
            )
            changed_message_driver_runs = select(SuperWorkspaceDriverRunRow.id).where(
                SuperWorkspaceDriverRunRow.user_id == normalized_user,
                SuperWorkspaceDriverRunRow.workspace_id == workspace.id,
                SuperWorkspaceDriverRunRow.updated_at > after,
            )
            statement = statement.where(
                or_(
                    SuperWorkspaceMessageRow.ingested_at > after,
                    SuperWorkspaceMessageRow.id.in_(changed_query_driver_runs),
                    SuperWorkspaceMessageRow.driver_run_id.in_(changed_message_driver_runs),
                )
            )
        elif before is not None:
            statement = statement.where(SuperWorkspaceMessageRow.occurred_at < before)
        statement = statement.order_by(SuperWorkspaceMessageRow.occurred_at.desc(), SuperWorkspaceMessageRow.id.desc())
        if after is None:
            statement = statement.limit(bounded_limit + 1)
        with self.read_session() as db:
            rows = list(db.scalars(statement).all())
            has_more = after is None and len(rows) > bounded_limit
            if after is None:
                rows = rows[:bounded_limit]
            items = self._display_items_from_rows(db, rows)
        next_after = max((item.updated_at for item in items), default=None)
        return SuperDisplayItemsPage(
            items=items,
            has_more=has_more,
            next_before=items[-1].created_at if has_more and items else None,
            next_after=next_after,
        )

    def visible_chat_history_context(
        self,
        user_id: str,
        workspace_id: str,
        chat_id: str,
        before_message_id: str | None,
        token_budget: int,
    ) -> str:
        if token_budget <= 0:
            return ""
        before_time = time.time()
        normalized_user = normalize_user_id(user_id)
        with self.read_session() as db:
            if before_message_id:
                current = db.scalar(
                    select(SuperWorkspaceMessageRow).where(
                        SuperWorkspaceMessageRow.id == before_message_id,
                        SuperWorkspaceMessageRow.user_id == normalized_user,
                    )
                )
                if current is not None:
                    before_time = float(current.occurred_at)
            query_condition = and_(
                SuperWorkspaceMessageRow.query.is_not(None),
                SuperWorkspaceMessageRow.query != "",
            )
            assistant_condition = and_(
                SuperWorkspaceMessageRow.role == "assistant",
                SuperWorkspaceMessageRow.event_type == "message:assistant",
                SuperWorkspaceMessageRow.text != "",
                ~self._agent_message_event_condition(),
            )
            rows = list(
                db.scalars(
                    select(SuperWorkspaceMessageRow)
                    .where(
                        SuperWorkspaceMessageRow.user_id == normalized_user,
                        SuperWorkspaceMessageRow.workspace_id == workspace_id,
                        SuperWorkspaceMessageRow.conversation_id == chat_id,
                        SuperWorkspaceMessageRow.occurred_at < before_time,
                        or_(query_condition, assistant_condition),
                    )
                    .order_by(SuperWorkspaceMessageRow.occurred_at.desc(), SuperWorkspaceMessageRow.id.desc())
                    .limit(200)
                ).all()
            )
            role_ids = {
                value
                for row in rows
                for value in (row.role_id, row.sender_role_id, row.recipient_role_id)
                if isinstance(value, str) and value
            }
            role_names: dict[str, str] = {}
            if role_ids:
                role_rows = db.execute(
                    select(SuperWorkspaceRoleRow.id, SuperWorkspaceRoleRow.name).where(
                        SuperWorkspaceRoleRow.user_id == normalized_user,
                        SuperWorkspaceRoleRow.workspace_id == workspace_id,
                        SuperWorkspaceRoleRow.id.in_(role_ids),
                    )
                ).all()
                role_names = {str(role_id): str(name) for role_id, name in role_rows}
        blocks: list[str] = []
        used = 0
        for row in rows:
            content = str(row.query or row.text or "").strip()
            if not content:
                continue
            sender = self._visible_message_sender_label(row, role_names)
            metadata = (
                f"Message ID: {row.id}\n"
                f"Sender: {sender}\n"
                f"Event type: {row.event_type}\n"
                f"Occurred at: {float(row.occurred_at):.3f}"
            )
            block = f"{metadata}\n\n{content}"
            count = rough_token_count(block)
            remaining = token_budget - used
            if count > remaining:
                if not blocks and remaining > 0:
                    block = trim_to_rough_tokens(block, remaining)
                    if block:
                        blocks.append(block)
                break
            blocks.append(block)
            used += count
        if not blocks:
            return ""
        ordered = list(reversed(blocks))
        return "Recent visible chat history before the current message:\n\n" + "\n\n---\n\n".join(ordered)

    @staticmethod
    def _visible_message_sender_label(row: SuperWorkspaceMessageRow, role_names: dict[str, str]) -> str:
        if isinstance(row.query, str) and row.query:
            if isinstance(row.sender_role_id, str) and row.sender_role_id:
                name = role_names.get(row.sender_role_id, row.sender_role_id)
                return f"query from {name} ({row.sender_role_id})"
            return "query from user"
        if isinstance(row.role_id, str) and row.role_id:
            name = role_names.get(row.role_id, row.role_id)
            return f"{name} ({row.role_id})"
        if isinstance(row.recipient_role_id, str) and row.recipient_role_id:
            name = role_names.get(row.recipient_role_id, row.recipient_role_id)
            return f"{name} ({row.recipient_role_id})"
        return str(row.role or "assistant")

    def get_super_run(self, run_id: str, user_id: str | None, message_limit: int = DEFAULT_MESSAGE_LIMIT) -> SuperHistoryRun:
        normalized_user = normalize_user_id(user_id)
        with self.read_session() as db:
            row = db.scalar(
                select(SuperWorkspaceMessageRow).where(
                    SuperWorkspaceMessageRow.id == run_id,
                    SuperWorkspaceMessageRow.user_id == normalized_user,
                    SuperWorkspaceMessageRow.query.is_not(None),
                    SuperWorkspaceMessageRow.query != "",
                )
            )
        if row is None:
            raise KeyError(run_id)
        return self._run_from_message(row, message_limit=message_limit)

    def _display_items_from_rows(self, db: Session, rows: list[SuperWorkspaceMessageRow]) -> list[SuperDisplayItem]:
        query_ids = {str(row.id) for row in rows if isinstance(row.query, str) and row.query}
        driver_ids = {str(row.driver_run_id) for row in rows if isinstance(row.driver_run_id, str) and row.driver_run_id}
        if not query_ids and not driver_ids:
            return []
        citation_ids_by_query: dict[str, list[str]] = {}
        target_chat_id_by_query_id: dict[str, str] = {}
        target_conditions = []
        if query_ids:
            target_conditions.append(SuperWorkspaceDriverRunRow.query_message_id.in_(query_ids))
        if driver_ids:
            target_conditions.append(SuperWorkspaceDriverRunRow.id.in_(driver_ids))
        target_rows = list(
            db.scalars(
                select(SuperWorkspaceDriverRunRow)
                .where(or_(*target_conditions))
                .order_by(SuperWorkspaceDriverRunRow.created_at.asc(), SuperWorkspaceDriverRunRow.id.asc())
            ).all()
        )
        targets_by_query: dict[str, list[SuperWorkspaceDriverRunRow]] = {}
        targets_by_id: dict[str, SuperWorkspaceDriverRunRow] = {}
        for target in target_rows:
            targets_by_query.setdefault(str(target.query_message_id), []).append(target)
            targets_by_id[str(target.id)] = target

        all_query_ids_for_targets = {
            str(target.query_message_id) for target in target_rows
        }
        query_message_ids = query_ids | all_query_ids_for_targets
        if query_message_ids:
            query_chat_rows = list(
                db.execute(
                    select(SuperWorkspaceMessageRow.id, SuperWorkspaceMessageRow.conversation_id).where(
                        SuperWorkspaceMessageRow.id.in_(query_message_ids)
                    )
                ).all()
            )
            for query_id, conversation_id in query_chat_rows:
                target_chat_id_by_query_id[str(query_id)] = str(conversation_id or "")

        if query_ids:
            citation_rows = list(
                db.execute(
                    select(
                        SuperWorkspaceMessageCitationRow.source_message_id,
                        SuperWorkspaceMessageCitationRow.cited_message_id,
                    )
                    .where(SuperWorkspaceMessageCitationRow.source_message_id.in_(query_ids))
                    .order_by(
                        SuperWorkspaceMessageCitationRow.source_message_id.asc(),
                        SuperWorkspaceMessageCitationRow.position.asc(),
                    )
                ).all()
            )
            for source_message_id, cited_message_id in citation_rows:
                citation_ids_by_query.setdefault(str(source_message_id), []).append(str(cited_message_id))

        return [
            self._display_item_from_row(
                row,
                targets_by_query,
                targets_by_id,
                citation_ids_by_query,
                target_chat_id_by_query_id,
            )
            for row in rows
            if not self._is_agent_message_event_row(row)
        ]

    @staticmethod
    def _agent_message_event_condition():
        return and_(
            SuperWorkspaceMessageRow.raw_json.like('%"type":"event_msg"%'),
            SuperWorkspaceMessageRow.raw_json.like('%"payload":{"type":"agent_message"%'),
        )

    def _is_agent_message_event_row(self, row: SuperWorkspaceMessageRow) -> bool:
        raw = self._parse_json(row.raw_json, {})
        payload = raw.get("payload") if isinstance(raw, dict) else None
        return (
            isinstance(raw, dict)
            and raw.get("type") == "event_msg"
            and isinstance(payload, dict)
            and payload.get("type") == "agent_message"
        )

    def record_provider_message(
        self,
        *,
        user_id: str,
            workspace_id: str | None,
        provider: str,
        viewer_session_id: str,
        provider_session_id: str | None,
        query_message_id: str | None,
        driver_run_id: str | None,
        parent_message_id: str | None,
        sender_role_id: str | None,
        recipient_role_id: str | None,
        role_id: str | None,
        event_index: int,
        received_at: float,
        source_path: str | None,
        source_event_id: str,
        source_line: int | None,
        role: str,
        event_type: str,
        text: str,
        raw: dict[str, Any],
        patch_text: str | None = None,
        file_changes: list[dict[str, Any]] | None = None,
    ) -> None:
        row_id = f"{provider}:{viewer_session_id}:{source_event_id}"
        with self.session_scope() as db:
            self._insert_message(
                db,
                {
                    "id": row_id,
                    "workspace_id": workspace_id,
                    "user_id": user_id,
                    "conversation_id": str(raw.get("chat_id") or raw.get("conversation_id") or ""),
                    "parent_message_id": parent_message_id,
                    "sender_role_id": sender_role_id,
                    "recipient_role_id": recipient_role_id,
                    "role_id": role_id,
                    "query_message_id": query_message_id,
                    "driver_run_id": driver_run_id,
                    "provider": provider,
                    "viewer_session_id": viewer_session_id,
                    "provider_session_id": provider_session_id,
                    "event_index": event_index,
                    "received_at": received_at,
                    "source_path": source_path,
                    "source_event_id": source_event_id,
                    "source_line": source_line,
                    "role": role,
                    "event_type": event_type,
                    "text": text,
                    "query": None,
                    "status": None,
                    "rationale": "",
                    "error": "",
                    "requested_role_ids_json": "[]",
                    "selected_role_ids_json": "[]",
                    "patch_text": patch_text,
                    "raw_json": self._json(raw),
                    "occurred_at": received_at,
                    "ingested_at": time.time(),
                    "file_changes": file_changes or [],
                },
            )
        if role == "assistant" and event_type == "message:assistant" and text.strip():
            retain_visible_message_background(
                user_id=user_id,
                workspace_id=workspace_id,
                chat_id=str(raw.get("chat_id") or raw.get("conversation_id") or ""),
                message_id=row_id,
                role=role,
                text=text,
                occurred_at=received_at,
                provider=provider,
                event_type=event_type,
                role_id=role_id,
                sender_role_id=sender_role_id,
                recipient_role_id=recipient_role_id,
            )

    def _display_item_from_row(
        self,
        row: SuperWorkspaceMessageRow,
        targets_by_query: dict[str, list[SuperWorkspaceDriverRunRow]],
        targets_by_id: dict[str, SuperWorkspaceDriverRunRow],
        citation_ids_by_query: dict[str, list[str]],
        target_chat_id_by_query_id: dict[str, str],
    ) -> SuperDisplayItem:
        is_query = isinstance(row.query, str) and bool(row.query)
        query_message_id = str(row.id) if is_query else row.query_message_id if isinstance(row.query_message_id, str) else None
        driver_run_id = row.driver_run_id if isinstance(row.driver_run_id, str) else None
        target = targets_by_id.get(driver_run_id or "")
        dispatch_targets = [
            self._display_target_from_row(item, target_chat_id_by_query_id) for item in targets_by_query.get(str(row.id), [])
        ] if is_query else []
        updated_at = float(row.ingested_at)
        if is_query and dispatch_targets:
            updated_at = max(updated_at, *(float(item.updated_at) for item in targets_by_query.get(str(row.id), [])))
        elif target is not None:
            updated_at = max(updated_at, float(target.updated_at))
        text_value = str(row.query or "") if is_query else str(row.text or "")
        return SuperDisplayItem(
            id=str(row.id),
            workspace_id=str(row.workspace_id or ""),
            chat_id=str(row.conversation_id or ""),
            kind="query" if is_query else "message",
            user_id=str(row.user_id),
            text=text_value,
            role=str(row.role),
            event_type=str(row.event_type),
            provider=str(row.provider),
            created_at=float(row.occurred_at),
            updated_at=updated_at,
            message_id=str(row.id),
            query_message_id=query_message_id,
            driver_run_id=driver_run_id,
            parent_message_id=row.parent_message_id if isinstance(row.parent_message_id, str) else None,
            sender_role_id=row.sender_role_id if isinstance(row.sender_role_id, str) else None,
            recipient_role_id=row.recipient_role_id if isinstance(row.recipient_role_id, str) else None,
            role_id=(str(target.role_id) if target is not None else row.role_id if isinstance(row.role_id, str) else None),
            role_name=str(target.role_name) if target is not None else "",
            viewer_session_id=str(target.viewer_session_id or "") if target is not None else str(row.viewer_session_id or ""),
            provider_session_id=(target.provider_session_id if isinstance(target.provider_session_id, str) else None) if target is not None else (row.provider_session_id if isinstance(row.provider_session_id, str) else None),
            session_ref=str(target.session_ref) if target is not None else "",
            model_context_window=int(target.model_context_window) if target is not None and isinstance(target.model_context_window, int) else None,
            total_tokens=int(target.total_tokens) if target is not None and isinstance(target.total_tokens, int) else None,
            context_used_percent=float(target.context_used_percent) if target is not None and isinstance(target.context_used_percent, (int, float)) else None,
            target_status=str(target.status) if target is not None else "",
            run_status=str(row.status or "") if is_query else "",
            error=str(row.error or "") if is_query else "",
            citation_ids=citation_ids_by_query.get(str(row.id), []) if is_query else [],
            dispatch_targets=dispatch_targets,
            raw=self._parse_json(row.raw_json, {}),
        )

    def _query_chat_id(self, query_message_id: str) -> str:
        with self.read_session() as db:
            row = db.scalar(select(SuperWorkspaceMessageRow.conversation_id).where(SuperWorkspaceMessageRow.id == query_message_id))
        return str(row or "")

    def _display_target_from_row(
        self,
        row: SuperWorkspaceDriverRunRow,
        target_chat_id_by_query_id: dict[str, str],
    ) -> SuperDisplayTarget:
        query_message_id = str(row.query_message_id)
        return SuperDisplayTarget(
            id=str(row.id),
            workspace_id=str(row.workspace_id),
            chat_id=target_chat_id_by_query_id.get(query_message_id, ""),
            role_id=str(row.role_id),
            role_name=str(row.role_name),
            provider=str(row.provider),
            viewer_session_id=str(row.viewer_session_id or ""),
            provider_session_id=row.provider_session_id if isinstance(row.provider_session_id, str) else None,
            session_ref=str(row.session_ref),
            status=str(row.status),
            model_context_window=int(row.model_context_window) if isinstance(row.model_context_window, int) else None,
            total_tokens=int(row.total_tokens) if isinstance(row.total_tokens, int) else None,
            context_used_percent=float(row.context_used_percent) if isinstance(row.context_used_percent, (int, float)) else None,
        )

    def _run_from_message(self, row: SuperWorkspaceMessageRow, message_limit: int = DEFAULT_MESSAGE_LIMIT) -> SuperHistoryRun:
        targets = [self._driver_from_row(target, message_limit=message_limit) for target in self._driver_rows(str(row.id))]
        selected_role_ids = [str(value) for value in self._parse_json(row.selected_role_ids_json, []) if isinstance(value, str)]
        target_role_ids = [target.role_id for target in targets]
        query = str(row.query or "")
        updated_at = max([float(row.ingested_at), *(target.updated_at for target in targets)])
        return SuperHistoryRun(
            id=str(row.id),
            workspace_id=str(row.workspace_id or DEFAULT_SUPER_WORKSPACE_ID),
            chat_id=str(row.conversation_id or ""),
            user_id=str(row.user_id),
            message=query,
            query=query,
            message_id=str(row.id),
            role_ids=target_role_ids or selected_role_ids,
            citation_ids=self.citation_ids_for_message(str(row.id)),
            status=str(row.status or "queued"),
            rationale=str(row.rationale or ""),
            error=str(row.error or ""),
            parent_message_id=row.parent_message_id if isinstance(row.parent_message_id, str) else None,
            sender_role_id=row.sender_role_id if isinstance(row.sender_role_id, str) else None,
            created_at=float(row.occurred_at),
            updated_at=updated_at,
            targets=targets,
        )

    def citation_ids_for_message(self, message_id: str) -> list[str]:
        with self.read_session() as db:
            rows = list(
                db.scalars(
                    select(SuperWorkspaceMessageCitationRow)
                    .where(SuperWorkspaceMessageCitationRow.source_message_id == message_id)
                    .order_by(SuperWorkspaceMessageCitationRow.position.asc())
                ).all()
            )
        return [str(row.cited_message_id) for row in rows]

    def cited_messages_for_query(self, user_id: str | None, query_message_id: str) -> list[AgentHistoryMessage]:
        normalized_user = normalize_user_id(user_id)
        with self.read_session() as db:
            rows = list(
                db.scalars(
                    select(SuperWorkspaceMessageCitationRow)
                    .where(SuperWorkspaceMessageCitationRow.source_message_id == query_message_id)
                    .order_by(SuperWorkspaceMessageCitationRow.position.asc())
                ).all()
            )
            cited_ids = [str(row.cited_message_id) for row in rows]
            if not cited_ids:
                return []
            messages = {
                str(row.id): self._message_from_row(row)
                for row in db.scalars(
                    select(SuperWorkspaceMessageRow).where(
                        SuperWorkspaceMessageRow.user_id == normalized_user,
                        SuperWorkspaceMessageRow.id.in_(cited_ids),
                    )
                ).all()
            }
        return [messages[message_id] for message_id in cited_ids if message_id in messages]

    def _driver_rows(self, query_message_id: str) -> list[SuperWorkspaceDriverRunRow]:
        with self.read_session() as db:
            return list(
                db.scalars(
                    select(SuperWorkspaceDriverRunRow)
                    .where(SuperWorkspaceDriverRunRow.query_message_id == query_message_id)
                    .order_by(SuperWorkspaceDriverRunRow.created_at, SuperWorkspaceDriverRunRow.id)
                ).all()
            )

    def _driver_from_row(self, row: SuperWorkspaceDriverRunRow, message_limit: int = DEFAULT_MESSAGE_LIMIT) -> SuperHistoryTarget:
        provider = str(row.provider)
        viewer_session_id = str(row.viewer_session_id)
        messages = self._driver_messages(row, message_limit=message_limit)
        updated_at = max([float(row.updated_at), *(message.occurred_at for message in messages)])
        return SuperHistoryTarget(
            id=str(row.id),
            workspace_id=str(row.workspace_id),
            chat_id=self._query_chat_id(str(row.query_message_id)),
            run_id=str(row.query_message_id),
            role_id=str(row.role_id),
            role_name=str(row.role_name),
            provider=provider,
            viewer_session_id=viewer_session_id,
            provider_session_id=row.provider_session_id if isinstance(row.provider_session_id, str) else None,
            session_ref=str(row.session_ref),
            agent_prompt=str(row.agent_prompt or ""),
            status=str(row.status),
            model_context_window=int(row.model_context_window) if isinstance(row.model_context_window, int) else None,
            total_tokens=int(row.total_tokens) if isinstance(row.total_tokens, int) else None,
            context_used_percent=float(row.context_used_percent) if isinstance(row.context_used_percent, (int, float)) else None,
            created_at=float(row.created_at),
            updated_at=updated_at,
            messages=messages,
        )

    def _dispatch_task_from_row(self, row: SuperWorkspaceDriverRunRow) -> SuperDispatchTask:
        return SuperDispatchTask(
            id=str(row.id),
            workspace_id=str(row.workspace_id),
            chat_id=self._query_chat_id(str(row.query_message_id)),
            query_message_id=str(row.query_message_id),
            user_id=str(row.user_id),
            role_id=str(row.role_id),
            role_name=str(row.role_name),
            provider=str(row.provider),
            viewer_session_id=str(row.viewer_session_id or ""),
            provider_session_id=row.provider_session_id if isinstance(row.provider_session_id, str) else None,
            session_ref=str(row.session_ref or ""),
            agent_prompt=str(row.agent_prompt or ""),
            status=str(row.status),
            parent_message_id=row.parent_message_id if isinstance(row.parent_message_id, str) else None,
            sender_role_id=row.sender_role_id if isinstance(row.sender_role_id, str) else None,
            recipient_role_id=str(row.recipient_role_id),
            role_snapshot=self._parse_json(row.role_snapshot_json, {}),
            claimed_by=row.claimed_by if isinstance(row.claimed_by, str) else None,
            claim_expires_at=float(row.claim_expires_at) if isinstance(row.claim_expires_at, (int, float)) else None,
            attempt_count=int(row.attempt_count or 0),
            next_attempt_at=float(row.next_attempt_at) if isinstance(row.next_attempt_at, (int, float)) else None,
            driver_pid=int(row.driver_pid) if isinstance(row.driver_pid, int) else None,
            driver_state_path=row.driver_state_path if isinstance(row.driver_state_path, str) else None,
            error=str(row.error or ""),
            start_after_occurred_at=float(row.start_after_occurred_at or 0),
            created_at=float(row.created_at),
            started_at=float(row.started_at) if isinstance(row.started_at, (int, float)) else None,
            finished_at=float(row.finished_at) if isinstance(row.finished_at, (int, float)) else None,
            updated_at=float(row.updated_at),
        )

    def _driver_messages(self, driver: SuperWorkspaceDriverRunRow, message_limit: int = DEFAULT_MESSAGE_LIMIT) -> list[AgentHistoryMessage]:
        driver_id = str(driver.id)
        with self.read_session() as db:
            rows = list(
                db.scalars(
                    select(SuperWorkspaceMessageRow)
                    .where(
                        SuperWorkspaceMessageRow.driver_run_id == driver_id,
                        SuperWorkspaceMessageRow.role != "user",
                    )
                    .order_by(SuperWorkspaceMessageRow.occurred_at.asc(), SuperWorkspaceMessageRow.id.asc())
                    .limit(max(1, min(message_limit, 300)))
                ).all()
            )
        return [self._message_from_row(row) for row in rows]

    def _message_from_row(self, row: SuperWorkspaceMessageRow) -> AgentHistoryMessage:
        query = row.query if isinstance(row.query, str) and row.query else None
        query_message_id = row.query_message_id if isinstance(row.query_message_id, str) else (str(row.id) if query else None)
        driver_run_id = row.driver_run_id if isinstance(row.driver_run_id, str) else None
        return AgentHistoryMessage(
            id=str(row.id),
            workspace_id=row.workspace_id if isinstance(row.workspace_id, str) else None,
            chat_id=str(row.conversation_id or ""),
            provider=str(row.provider),
            viewer_session_id=row.viewer_session_id if isinstance(row.viewer_session_id, str) else None,
            provider_session_id=row.provider_session_id if isinstance(row.provider_session_id, str) else None,
            index=int(row.event_index),
            received_at=float(row.received_at),
            role=str(row.role),
            event_type=str(row.event_type),
            text=str(row.text or ""),
            query=query,
            status=row.status if isinstance(row.status, str) else None,
            rationale=str(row.rationale or ""),
            error=str(row.error or ""),
            requested_role_ids=[str(value) for value in self._parse_json(row.requested_role_ids_json, []) if isinstance(value, str)],
            selected_role_ids=[str(value) for value in self._parse_json(row.selected_role_ids_json, []) if isinstance(value, str)],
            file_changes=self._file_changes_for_message(str(row.id)),
            patch_text=row.patch_text if isinstance(row.patch_text, str) else None,
            raw=self._parse_json(row.raw_json, {}),
            occurred_at=float(row.occurred_at),
            query_id=query_message_id,
            query_message_id=query_message_id,
            driver_run_id=driver_run_id,
            super_run_id=query_message_id,
            super_target_id=driver_run_id,
            parent_message_id=row.parent_message_id if isinstance(row.parent_message_id, str) else None,
            sender_role_id=row.sender_role_id if isinstance(row.sender_role_id, str) else None,
            recipient_role_id=row.recipient_role_id if isinstance(row.recipient_role_id, str) else None,
        )

    def _insert_message(self, db: Session, row: dict[str, Any]) -> None:
        file_changes = row.pop("file_changes", [])
        statement = sqlite_insert(SuperWorkspaceMessageRow).values(**row)
        db.execute(
            statement.on_conflict_do_update(
                index_elements=["provider", "viewer_session_id", "source_event_id"],
                set_={key: statement.excluded[key] for key in row.keys() if key != "id"},
            )
        )
        self._replace_file_changes(db, str(row["id"]), file_changes)

    def _replace_citations(self, db: Session, source_message_id: str, citation_ids: list[str], created_at: float) -> None:
        db.execute(delete(SuperWorkspaceMessageCitationRow).where(SuperWorkspaceMessageCitationRow.source_message_id == source_message_id))
        for position, cited_message_id in enumerate(self._normalize_citation_ids(citation_ids)):
            db.add(
                SuperWorkspaceMessageCitationRow(
                    source_message_id=source_message_id,
                    position=position,
                    cited_message_id=cited_message_id,
                    created_at=created_at,
                )
            )

    def _validate_citation_ids(self, db: Session, user_id: str, citation_ids: list[str]) -> None:
        if not citation_ids:
            return
        found = set(
            db.scalars(
                select(SuperWorkspaceMessageRow.id).where(
                    SuperWorkspaceMessageRow.user_id == user_id,
                    SuperWorkspaceMessageRow.id.in_(citation_ids),
                )
            ).all()
        )
        missing = [message_id for message_id in citation_ids if message_id not in found]
        if missing:
            raise ValueError(f"Unknown cited Super Workspace message id: {missing[0]}")

    @staticmethod
    def _normalize_citation_ids(citation_ids: list[str]) -> list[str]:
        normalized: list[str] = []
        for value in citation_ids:
            message_id = str(value).strip()
            if message_id and message_id not in normalized:
                normalized.append(message_id)
        return normalized

    def _replace_file_changes(self, db: Session, message_id: str, file_changes: Any) -> None:
        db.execute(delete(SuperWorkspaceMessageFileChangeRow).where(SuperWorkspaceMessageFileChangeRow.message_id == message_id))
        if not isinstance(file_changes, list):
            return
        for position, item in enumerate(file_changes):
            if not isinstance(item, dict):
                continue
            path = item.get("path")
            change_type = item.get("change_type")
            if not isinstance(path, str) or not isinstance(change_type, str):
                continue
            diff = item.get("diff") if isinstance(item.get("diff"), str) else None
            db.add(
                SuperWorkspaceMessageFileChangeRow(
                    message_id=message_id,
                    position=position,
                    path=path,
                    change_type=change_type,
                    diff=diff,
                )
            )

    def _file_changes_for_message(self, message_id: str) -> list[AgentHistoryFileChange]:
        with self.read_session() as db:
            rows = list(
                db.scalars(
                    select(SuperWorkspaceMessageFileChangeRow)
                    .where(SuperWorkspaceMessageFileChangeRow.message_id == message_id)
                    .order_by(SuperWorkspaceMessageFileChangeRow.position)
                ).all()
            )
        return [AgentHistoryFileChange(path=str(row.path), change_type=str(row.change_type), diff=row.diff if isinstance(row.diff, str) else None) for row in rows]

    def _latest_session_message(self, provider: str, viewer_session_id: str) -> SuperWorkspaceMessageRow | None:
        with self.read_session() as db:
            return db.scalar(
                select(SuperWorkspaceMessageRow)
                .where(SuperWorkspaceMessageRow.provider == provider, SuperWorkspaceMessageRow.viewer_session_id == viewer_session_id)
                .order_by(SuperWorkspaceMessageRow.occurred_at.desc(), SuperWorkspaceMessageRow.id.desc())
                .limit(1)
            )

    def _provider_session_id(self, provider: str, viewer_session_id: str) -> str | None:
        return None

    def _parse_session_ref(self, session_ref: str, fallback_provider: str) -> tuple[str, str]:
        if ":" in session_ref:
            provider, session_id = session_ref.split(":", 1)
            return provider or fallback_provider, session_id
        return fallback_provider, session_ref

    def _json(self, value: Any) -> str:
        return json.dumps(value, ensure_ascii=False, separators=(",", ":"), default=str)

    def _parse_json(self, value: Any, default: Any) -> Any:
        if not isinstance(value, str) or not value:
            return default
        try:
            return json.loads(value)
        except json.JSONDecodeError:
            return default


agent_history_store = AgentHistoryStore()
