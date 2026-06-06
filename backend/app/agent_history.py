from __future__ import annotations

import json
import time
import uuid
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path
from typing import Any

from pydantic import BaseModel, Field
from sqlalchemy import Float, ForeignKey, Index, Integer, String, Text, UniqueConstraint, and_, create_engine, delete, or_, select, text, update
from sqlalchemy.dialects.sqlite import insert as sqlite_insert
from sqlalchemy.orm import DeclarativeBase, Mapped, Session, mapped_column, sessionmaker
from sqlalchemy.pool import NullPool

from .storage import AGENT_HISTORY_DB_PATH
from .users import normalize_user_id

SUPER_WORKSPACE_PROVIDER = "super_workspace"
DEFAULT_RUN_LIMIT = 30
DEFAULT_MESSAGE_LIMIT = 120


class SuperHistoryRunCreate(BaseModel):
    message: str
    parent_message_id: str | None = None
    sender_role_id: str | None = None


class SuperDriverRunCreate(BaseModel):
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
    run_id: str
    role_id: str
    role_name: str
    provider: str
    viewer_session_id: str
    session_ref: str
    agent_prompt: str = ""
    status: str = "queued"
    created_at: float
    updated_at: float
    messages: list[AgentHistoryMessage] = Field(default_factory=list)


class SuperHistoryRun(BaseModel):
    id: str
    user_id: str
    message: str
    query: str
    message_id: str
    role_ids: list[str] = Field(default_factory=list)
    status: str
    rationale: str = ""
    error: str = ""
    parent_message_id: str | None = None
    sender_role_id: str | None = None
    created_at: float
    updated_at: float
    targets: list[SuperHistoryTarget] = Field(default_factory=list)


class SuperHistoryRunsPage(BaseModel):
    runs: list[SuperHistoryRun]
    has_more: bool = False
    next_before: float | None = None
    next_after: float | None = None


class AgentHistoryBase(DeclarativeBase):
    pass


class SuperWorkspaceMessageRow(AgentHistoryBase):
    __tablename__ = "super_workspace_messages"
    __table_args__ = (
        UniqueConstraint("provider", "viewer_session_id", "source_event_id"),
        Index("idx_super_messages_user_query_time", "user_id", "query", "occurred_at", "id"),
        Index("idx_super_messages_query_message", "query_message_id", "occurred_at", "id"),
        Index("idx_super_messages_driver_time", "driver_run_id", "occurred_at", "id"),
        Index("idx_super_messages_session_time", "provider", "viewer_session_id", "occurred_at", "id"),
    )

    id: Mapped[str] = mapped_column(String, primary_key=True)
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
    )

    id: Mapped[str] = mapped_column(String, primary_key=True)
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
        self.engine = create_engine(f"sqlite:///{self.path.as_posix()}", future=True, poolclass=NullPool)
        self.SessionLocal = sessionmaker(self.engine, expire_on_commit=False, future=True)
        self._ensure_schema()

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
        AgentHistoryBase.metadata.create_all(self.engine)
        self._ensure_driver_run_columns()

    def _ensure_driver_run_columns(self) -> None:
        columns = {
            "role_snapshot_json": "TEXT NOT NULL DEFAULT '{}'",
            "claimed_by": "TEXT",
            "claim_expires_at": "REAL",
            "attempt_count": "INTEGER NOT NULL DEFAULT 0",
            "next_attempt_at": "REAL",
            "driver_pid": "INTEGER",
            "driver_state_path": "TEXT",
            "error": "TEXT NOT NULL DEFAULT ''",
            "started_at": "REAL",
            "finished_at": "REAL",
        }
        with self.engine.begin() as connection:
            existing = {
                str(row[1])
                for row in connection.exec_driver_sql("PRAGMA table_info(super_workspace_driver_runs)").all()
            }
            for name, definition in columns.items():
                if name not in existing:
                    connection.execute(text(f"ALTER TABLE super_workspace_driver_runs ADD COLUMN {name} {definition}"))

    def create_super_run(
        self,
        user_id: str | None,
        message: str,
        status: str,
        role_ids: list[str] | None = None,
        rationale: str = "",
        raw: dict[str, Any] | None = None,
        parent_message_id: str | None = None,
        sender_role_id: str | None = None,
    ) -> SuperHistoryRun:
        normalized_user = normalize_user_id(user_id)
        now = time.time()
        message_id = uuid.uuid4().hex
        raw_message = raw or {
            "type": "super_workspace_query_message",
            "message_id": message_id,
            "query": message,
            "role_ids": role_ids or [],
            "parent_message_id": parent_message_id,
            "sender_role_id": sender_role_id,
        }
        with self.session_scope() as db:
            self._insert_message(
                db,
                {
                    "id": message_id,
                    "user_id": normalized_user,
                    "conversation_id": "default",
                    "parent_message_id": parent_message_id,
                    "sender_role_id": sender_role_id,
                    "recipient_role_id": None,
                    "role_id": sender_role_id or "user",
                    "query_message_id": None,
                    "driver_run_id": None,
                    "provider": SUPER_WORKSPACE_PROVIDER,
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
        return self.get_super_run(message_id, normalized_user)

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
        with self.session_scope() as db:
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
            row.ingested_at = now
            if status is not None:
                row.status = status
            if role_ids is not None:
                row.selected_role_ids_json = self._json(role_ids)
            if rationale is not None:
                row.rationale = rationale
            if error is not None:
                row.error = error
        return self.get_super_run(run_id, normalized_user)

    def record_super_target(self, user_id: str | None, run_id: str, request: SuperDriverRunCreate) -> SuperHistoryRun:
        return self.create_dispatch_task(user_id, run_id, request)

    def create_dispatch_task(self, user_id: str | None, query_message_id: str, request: SuperDriverRunCreate) -> SuperHistoryRun:
        normalized_user = normalize_user_id(user_id)
        provider, viewer_session_id = self._parse_session_ref(request.session_ref, request.provider)
        provider_session_id = self._provider_session_id(provider, viewer_session_id)
        now = time.time()
        start = self._latest_session_message(provider, viewer_session_id) if viewer_session_id else None
        with self.session_scope() as db:
            query = db.scalar(
                select(SuperWorkspaceMessageRow).where(
                    SuperWorkspaceMessageRow.id == query_message_id,
                    SuperWorkspaceMessageRow.user_id == normalized_user,
                    SuperWorkspaceMessageRow.query.is_not(None),
                    SuperWorkspaceMessageRow.query != "",
                )
            )
            if query is None:
                raise KeyError(query_message_id)
            existing = db.scalar(
                select(SuperWorkspaceDriverRunRow).where(
                    SuperWorkspaceDriverRunRow.query_message_id == query_message_id,
                    SuperWorkspaceDriverRunRow.role_id == request.role_id,
                    SuperWorkspaceDriverRunRow.provider == provider,
                )
            )
            if existing is None:
                db.add(
                    SuperWorkspaceDriverRunRow(
                        id=uuid.uuid4().hex,
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
                        sender_role_id=request.sender_role_id or query.sender_role_id,
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
    ) -> None:
        values: dict[str, Any] = {"status": status, "updated_at": time.time()}
        if session_ref is not None:
            provider, viewer_session_id = self._parse_session_ref(session_ref, "")
            values["session_ref"] = session_ref
            values["provider"] = provider
            values["viewer_session_id"] = viewer_session_id
            values["provider_session_id"] = self._provider_session_id(provider, viewer_session_id)
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
            db.execute(
                update(SuperWorkspaceDriverRunRow)
                .where(SuperWorkspaceDriverRunRow.id == driver_run_id)
                .values(**values)
            )

    def claim_next_dispatch_task(self, worker_id: str, lease_seconds: float = 60.0) -> SuperDispatchTask | None:
        now = time.time()
        with self.session_scope() as db:
            db.execute(
                update(SuperWorkspaceDriverRunRow)
                .where(
                    SuperWorkspaceDriverRunRow.status == "claimed",
                    SuperWorkspaceDriverRunRow.claim_expires_at.is_not(None),
                    SuperWorkspaceDriverRunRow.claim_expires_at <= now,
                )
                .values(status="queued", claimed_by=None, claim_expires_at=None, updated_at=now)
            )
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
                active = db.scalar(
                    select(SuperWorkspaceDriverRunRow.id)
                    .where(
                        SuperWorkspaceDriverRunRow.id != row.id,
                        SuperWorkspaceDriverRunRow.user_id == row.user_id,
                        SuperWorkspaceDriverRunRow.role_id == row.role_id,
                        or_(
                            SuperWorkspaceDriverRunRow.status == "running",
                            and_(
                                SuperWorkspaceDriverRunRow.status == "claimed",
                                or_(SuperWorkspaceDriverRunRow.claim_expires_at.is_(None), SuperWorkspaceDriverRunRow.claim_expires_at > now),
                            ),
                        ),
                    )
                    .limit(1)
                )
                if active:
                    continue
                row.status = "claimed"
                row.claimed_by = worker_id
                row.claim_expires_at = now + lease_seconds
                row.attempt_count = int(row.attempt_count or 0) + 1
                row.updated_at = now
                return self._dispatch_task_from_row(row)
        return None

    def get_dispatch_task(self, task_id: str) -> SuperDispatchTask | None:
        with self.read_session() as db:
            row = db.scalar(select(SuperWorkspaceDriverRunRow).where(SuperWorkspaceDriverRunRow.id == task_id))
            return self._dispatch_task_from_row(row) if row is not None else None

    def summarize_super_run_status(self, run_id: str, user_id: str | None, fallback_error: str = "") -> SuperHistoryRun:
        run = self.get_super_run(run_id, user_id)
        statuses = [target.status for target in run.targets]
        if any(status == "failed" for status in statuses):
            return self.update_super_run(run_id, user_id, status="failed", error=fallback_error)
        if statuses and all(status == "completed" for status in statuses):
            return self.update_super_run(run_id, user_id, status="completed", error="")
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
        bounded_limit = max(1, min(limit, 100))
        statement = (
            select(SuperWorkspaceMessageRow)
            .where(
                SuperWorkspaceMessageRow.user_id == normalized_user,
                SuperWorkspaceMessageRow.query.is_not(None),
                SuperWorkspaceMessageRow.query != "",
            )
        )
        if after is not None:
            changed_driver_runs = select(SuperWorkspaceDriverRunRow.query_message_id).where(
                SuperWorkspaceDriverRunRow.user_id == normalized_user,
                SuperWorkspaceDriverRunRow.updated_at > after,
            )
            statement = statement.where(
                or_(
                    SuperWorkspaceMessageRow.ingested_at > after,
                    SuperWorkspaceMessageRow.id.in_(changed_driver_runs),
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

    def _run_from_message(self, row: SuperWorkspaceMessageRow, message_limit: int = DEFAULT_MESSAGE_LIMIT) -> SuperHistoryRun:
        targets = [self._driver_from_row(target, message_limit=message_limit) for target in self._driver_rows(str(row.id))]
        selected_role_ids = [str(value) for value in self._parse_json(row.selected_role_ids_json, []) if isinstance(value, str)]
        target_role_ids = [target.role_id for target in targets]
        query = str(row.query or "")
        updated_at = max([float(row.ingested_at), *(target.updated_at for target in targets)])
        return SuperHistoryRun(
            id=str(row.id),
            user_id=str(row.user_id),
            message=query,
            query=query,
            message_id=str(row.id),
            role_ids=target_role_ids or selected_role_ids,
            status=str(row.status or "queued"),
            rationale=str(row.rationale or ""),
            error=str(row.error or ""),
            parent_message_id=row.parent_message_id if isinstance(row.parent_message_id, str) else None,
            sender_role_id=row.sender_role_id if isinstance(row.sender_role_id, str) else None,
            created_at=float(row.occurred_at),
            updated_at=updated_at,
            targets=targets,
        )

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
            run_id=str(row.query_message_id),
            role_id=str(row.role_id),
            role_name=str(row.role_name),
            provider=provider,
            viewer_session_id=viewer_session_id,
            session_ref=str(row.session_ref),
            agent_prompt=str(row.agent_prompt or ""),
            status=str(row.status),
            created_at=float(row.created_at),
            updated_at=updated_at,
            messages=messages,
        )

    def _dispatch_task_from_row(self, row: SuperWorkspaceDriverRunRow) -> SuperDispatchTask:
        return SuperDispatchTask(
            id=str(row.id),
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
        provider = str(driver.provider)
        viewer_session_id = str(driver.viewer_session_id)
        driver_id = str(driver.id)
        with self.read_session() as db:
            linked_rows = list(
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
            if linked_rows:
                return [self._message_from_row(row) for row in linked_rows]
            start = self._driver_anchor_time(db, driver)
            if start is None:
                return []
            next_drivers = list(
                db.scalars(
                    select(SuperWorkspaceDriverRunRow)
                    .where(
                        SuperWorkspaceDriverRunRow.provider == provider,
                        SuperWorkspaceDriverRunRow.viewer_session_id == viewer_session_id,
                        SuperWorkspaceDriverRunRow.id != driver_id,
                    )
                    .order_by(SuperWorkspaceDriverRunRow.created_at.asc(), SuperWorkspaceDriverRunRow.id.asc())
                ).all()
            )
            next_starts: list[float] = []
            for item in next_drivers:
                next_start = self._driver_anchor_time(db, item)
                if next_start is not None and next_start > start:
                    next_starts.append(next_start)
            update_conditions = [
                SuperWorkspaceMessageRow.provider == provider,
                SuperWorkspaceMessageRow.viewer_session_id == viewer_session_id,
                SuperWorkspaceMessageRow.occurred_at >= start,
                SuperWorkspaceMessageRow.role != "user",
            ]
            select_conditions = list(update_conditions)
            if next_starts:
                upper = min(next_starts)
                update_conditions.append(SuperWorkspaceMessageRow.occurred_at < upper)
                select_conditions.append(SuperWorkspaceMessageRow.occurred_at < upper)
            rows = list(
                db.scalars(
                    select(SuperWorkspaceMessageRow)
                    .where(*select_conditions)
                    .order_by(SuperWorkspaceMessageRow.occurred_at.asc(), SuperWorkspaceMessageRow.id.asc())
                    .limit(max(1, min(message_limit, 300)))
                ).all()
            )
        return [self._message_from_row(row) for row in rows]

    def _driver_anchor_time(self, db: Session, driver: SuperWorkspaceDriverRunRow) -> float | None:
        provider = str(driver.provider)
        viewer_session_id = str(driver.viewer_session_id)
        start = float(driver.start_after_occurred_at or driver.created_at)
        prompt = str(driver.agent_prompt or "").strip()
        if not prompt:
            return start
        occurred_at = db.scalar(
            select(SuperWorkspaceMessageRow.occurred_at)
            .where(
                SuperWorkspaceMessageRow.provider == provider,
                SuperWorkspaceMessageRow.viewer_session_id == viewer_session_id,
                SuperWorkspaceMessageRow.role == "user",
                SuperWorkspaceMessageRow.text == prompt,
                SuperWorkspaceMessageRow.occurred_at >= start,
            )
            .order_by(SuperWorkspaceMessageRow.occurred_at.asc(), SuperWorkspaceMessageRow.id.asc())
            .limit(1)
        )
        if occurred_at is None:
            return None
        return float(occurred_at)

    def _message_from_row(self, row: SuperWorkspaceMessageRow) -> AgentHistoryMessage:
        query = row.query if isinstance(row.query, str) and row.query else None
        query_message_id = row.query_message_id if isinstance(row.query_message_id, str) else (str(row.id) if query else None)
        driver_run_id = row.driver_run_id if isinstance(row.driver_run_id, str) else None
        return AgentHistoryMessage(
            id=str(row.id),
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
