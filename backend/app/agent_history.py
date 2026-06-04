from __future__ import annotations

import hashlib
import json
import sqlite3
import time
import uuid
from pathlib import Path
from typing import Any

from pydantic import BaseModel, Field

from .codex_sessions import codex_session_manager
from .hermes_sessions import HERMES_STATE_DB, hermes_session_manager
from .models import AgentEventType
from .storage import AGENT_HISTORY_DB_PATH
from .users import normalize_user_id

SCHEMA_VERSION = 11
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
    session_ref: str
    agent_prompt: str = ""
    parent_message_id: str | None = None
    sender_role_id: str | None = None


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


class AgentHistoryStore:
    def __init__(self, path: Path = AGENT_HISTORY_DB_PATH) -> None:
        self.path = path

    def connect(self) -> sqlite3.Connection:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        connection = sqlite3.connect(self.path, timeout=10.0)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA journal_mode=WAL")
        connection.execute("PRAGMA foreign_keys=ON")
        self._ensure_schema(connection)
        return connection

    def _ensure_schema(self, connection: sqlite3.Connection) -> None:
        current_version = int(connection.execute("PRAGMA user_version").fetchone()[0])
        if current_version != SCHEMA_VERSION:
            self._reset_schema(connection)
        connection.executescript(
            """
            CREATE TABLE IF NOT EXISTS super_workspace_messages (
              id TEXT PRIMARY KEY,
              user_id TEXT NOT NULL,
              conversation_id TEXT NOT NULL,
              parent_message_id TEXT,
              sender_role_id TEXT,
              recipient_role_id TEXT,
              role_id TEXT,
              query_message_id TEXT,
              driver_run_id TEXT,
              provider TEXT NOT NULL,
              viewer_session_id TEXT,
              provider_session_id TEXT,
              event_index INTEGER NOT NULL,
              received_at REAL NOT NULL,
              source_path TEXT,
              source_event_id TEXT NOT NULL,
              source_line INTEGER,
              role TEXT NOT NULL,
              event_type TEXT NOT NULL,
              text TEXT NOT NULL,
              query TEXT,
              status TEXT,
              rationale TEXT NOT NULL DEFAULT '',
              error TEXT NOT NULL DEFAULT '',
              requested_role_ids_json TEXT NOT NULL DEFAULT '[]',
              selected_role_ids_json TEXT NOT NULL DEFAULT '[]',
              patch_text TEXT,
              raw_json TEXT NOT NULL,
              occurred_at REAL NOT NULL,
              ingested_at REAL NOT NULL,
              UNIQUE(provider, viewer_session_id, source_event_id)
            );

            CREATE TABLE IF NOT EXISTS super_workspace_driver_runs (
              id TEXT PRIMARY KEY,
              query_message_id TEXT NOT NULL,
              user_id TEXT NOT NULL,
              role_id TEXT NOT NULL,
              role_name TEXT NOT NULL,
              provider TEXT NOT NULL,
              viewer_session_id TEXT NOT NULL,
              provider_session_id TEXT,
              session_ref TEXT NOT NULL,
              agent_prompt TEXT NOT NULL,
              status TEXT NOT NULL,
              parent_message_id TEXT,
              sender_role_id TEXT,
              recipient_role_id TEXT NOT NULL,
              start_after_occurred_at REAL NOT NULL,
              created_at REAL NOT NULL,
              updated_at REAL NOT NULL,
              FOREIGN KEY (query_message_id) REFERENCES super_workspace_messages(id) ON DELETE CASCADE
            );

            CREATE TABLE IF NOT EXISTS super_workspace_message_file_changes (
              message_id TEXT NOT NULL,
              position INTEGER NOT NULL,
              path TEXT NOT NULL,
              change_type TEXT NOT NULL,
              diff TEXT,
              PRIMARY KEY (message_id, position),
              FOREIGN KEY (message_id) REFERENCES super_workspace_messages(id) ON DELETE CASCADE
            );

            CREATE TABLE IF NOT EXISTS super_workspace_driver_checkpoints (
              driver_run_id TEXT PRIMARY KEY,
              provider TEXT NOT NULL,
              viewer_session_id TEXT NOT NULL,
              source_path TEXT,
              byte_offset INTEGER NOT NULL DEFAULT 0,
              line_count INTEGER NOT NULL DEFAULT 0,
              last_source_event_id TEXT,
              updated_at REAL NOT NULL,
              FOREIGN KEY (driver_run_id) REFERENCES super_workspace_driver_runs(id) ON DELETE CASCADE
            );

            CREATE INDEX IF NOT EXISTS idx_super_messages_user_query_time
              ON super_workspace_messages(user_id, query, occurred_at DESC, id DESC);
            CREATE INDEX IF NOT EXISTS idx_super_messages_query_message
              ON super_workspace_messages(query_message_id, occurred_at, id);
            CREATE INDEX IF NOT EXISTS idx_super_messages_driver_time
              ON super_workspace_messages(driver_run_id, occurred_at, id);
            CREATE INDEX IF NOT EXISTS idx_super_messages_session_time
              ON super_workspace_messages(provider, viewer_session_id, occurred_at, id);
            CREATE INDEX IF NOT EXISTS idx_super_driver_runs_query_message
              ON super_workspace_driver_runs(query_message_id, created_at, id);
            """
        )
        connection.execute(f"PRAGMA user_version = {SCHEMA_VERSION}")
        connection.commit()

    def _reset_schema(self, connection: sqlite3.Connection) -> None:
        connection.executescript(
            """
            DROP TABLE IF EXISTS super_workspace_driver_checkpoints;
            DROP TABLE IF EXISTS super_workspace_message_file_changes;
            DROP TABLE IF EXISTS super_workspace_driver_runs;
            DROP TABLE IF EXISTS super_workspace_messages;
            DROP TABLE IF EXISTS super_workspace_queries;
            DROP TABLE IF EXISTS super_workspace_run_targets;
            DROP TABLE IF EXISTS super_workspace_runs;
            DROP TABLE IF EXISTS agent_message_file_changes;
            DROP TABLE IF EXISTS agent_messages;
            DROP TABLE IF EXISTS agent_sources;
            """
        )

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
        with self.connect() as connection:
            self._insert_message(
                connection,
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
            connection.commit()
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
        with self.connect() as connection:
            row = connection.execute(
                "SELECT id FROM super_workspace_messages WHERE id = ? AND user_id = ? AND COALESCE(query, '') != ''",
                (run_id, normalized_user),
            ).fetchone()
            if row is None:
                raise KeyError(run_id)
            updates = ["ingested_at = ?"]
            values: list[Any] = [now]
            if status is not None:
                updates.append("status = ?")
                values.append(status)
            if role_ids is not None:
                updates.append("selected_role_ids_json = ?")
                values.append(self._json(role_ids))
            if rationale is not None:
                updates.append("rationale = ?")
                values.append(rationale)
            if error is not None:
                updates.append("error = ?")
                values.append(error)
            values.extend([run_id, normalized_user])
            connection.execute(f"UPDATE super_workspace_messages SET {', '.join(updates)} WHERE id = ? AND user_id = ?", values)
            connection.commit()
        return self.get_super_run(run_id, normalized_user)

    def record_super_target(self, user_id: str | None, run_id: str, request: SuperDriverRunCreate) -> SuperHistoryRun:
        return self.create_driver_run(user_id, run_id, request)

    def create_driver_run(self, user_id: str | None, query_message_id: str, request: SuperDriverRunCreate) -> SuperHistoryRun:
        normalized_user = normalize_user_id(user_id)
        provider, viewer_session_id = self._parse_session_ref(request.session_ref, request.provider)
        provider_session_id = self._provider_session_id(provider, viewer_session_id)
        now = time.time()
        start = self._latest_session_message(provider, viewer_session_id)
        with self.connect() as connection:
            query = connection.execute(
                """
                SELECT id, sender_role_id
                FROM super_workspace_messages
                WHERE id = ? AND user_id = ? AND COALESCE(query, '') != ''
                """,
                (query_message_id, normalized_user),
            ).fetchone()
            if query is None:
                raise KeyError(query_message_id)
            existing = connection.execute(
                """
                SELECT id FROM super_workspace_driver_runs
                WHERE query_message_id = ? AND role_id = ? AND provider = ? AND viewer_session_id = ?
                """,
                (query_message_id, request.role_id, provider, viewer_session_id),
            ).fetchone()
            if existing is None:
                connection.execute(
                    """
                    INSERT INTO super_workspace_driver_runs (
                      id, query_message_id, user_id, role_id, role_name, provider,
                      viewer_session_id, provider_session_id, session_ref, agent_prompt,
                      status, parent_message_id, sender_role_id, recipient_role_id,
                      start_after_occurred_at, created_at, updated_at
                    )
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        uuid.uuid4().hex,
                        query_message_id,
                        normalized_user,
                        request.role_id,
                        request.role_name,
                        provider,
                        viewer_session_id,
                        provider_session_id,
                        request.session_ref,
                        request.agent_prompt,
                        request.parent_message_id or query_message_id,
                        request.sender_role_id or (str(query["sender_role_id"]) if isinstance(query["sender_role_id"], str) else None),
                        request.role_id,
                        float(start["occurred_at"]) if start else now,
                        now,
                        now,
                    ),
                )
            connection.commit()
        return self.get_super_run(query_message_id, normalized_user, sync_targets=False)

    def update_driver_run_status(self, driver_run_id: str, status: str) -> None:
        with self.connect() as connection:
            connection.execute("UPDATE super_workspace_driver_runs SET status = ?, updated_at = ? WHERE id = ?", (status, time.time(), driver_run_id))
            connection.commit()

    def list_super_runs(self, user_id: str | None, limit: int = DEFAULT_RUN_LIMIT, before: float | None = None, message_limit: int = DEFAULT_MESSAGE_LIMIT) -> SuperHistoryRunsPage:
        normalized_user = normalize_user_id(user_id)
        bounded_limit = max(1, min(limit, 100))
        params: list[Any] = [normalized_user]
        where = "WHERE user_id = ? AND COALESCE(query, '') != ''"
        if before is not None:
            where += " AND occurred_at < ?"
            params.append(before)
        with self.connect() as connection:
            rows = connection.execute(
                f"""
                SELECT * FROM super_workspace_messages
                {where}
                ORDER BY occurred_at DESC, id DESC
                LIMIT ?
                """,
                (*params, bounded_limit + 1),
            ).fetchall()
        has_more = len(rows) > bounded_limit
        rows = rows[:bounded_limit]
        runs = [self._run_from_message(row, message_limit=message_limit) for row in rows]
        return SuperHistoryRunsPage(runs=runs, has_more=has_more, next_before=runs[-1].created_at if has_more and runs else None)

    def get_super_run(self, run_id: str, user_id: str | None, sync_targets: bool = False, message_limit: int = DEFAULT_MESSAGE_LIMIT) -> SuperHistoryRun:
        normalized_user = normalize_user_id(user_id)
        with self.connect() as connection:
            row = connection.execute(
                """
                SELECT * FROM super_workspace_messages
                WHERE id = ? AND user_id = ? AND COALESCE(query, '') != ''
                """,
                (run_id, normalized_user),
            ).fetchone()
        if row is None:
            raise KeyError(run_id)
        if sync_targets:
            for target in self._driver_rows(run_id):
                self.sync_session(str(target["provider"]), str(target["viewer_session_id"]), normalized_user)
        return self._run_from_message(row, message_limit=message_limit)

    def sync_super_workspace(self, user_id: str | None) -> None:
        normalized_user = normalize_user_id(user_id)
        with self.connect() as connection:
            rows = connection.execute(
                "SELECT DISTINCT provider, viewer_session_id FROM super_workspace_driver_runs WHERE user_id = ?",
                (normalized_user,),
            ).fetchall()
        for row in rows:
            self.sync_session(str(row["provider"]), str(row["viewer_session_id"]), normalized_user)

    def sync_session(self, provider: str, viewer_session_id: str, user_id: str | None = None) -> None:
        if provider == "codex":
            self._sync_codex_session(viewer_session_id, user_id)
        elif provider == "hermes":
            self._sync_hermes_session(viewer_session_id, user_id)

    def _sync_codex_session(self, viewer_session_id: str, user_id: str | None) -> None:
        try:
            session = codex_session_manager.get(viewer_session_id)
        except Exception:
            return
        normalized_user = normalize_user_id(user_id or session.user_id)
        compact_events = codex_session_manager._compact_events(session.events)
        raw_by_index = {event.get("index"): event for event in session.events}
        path = session.rollout_path.as_posix() if session.rollout_path else None
        with self.connect() as connection:
            for prompt_index, prompt in enumerate(session.prompts):
                text = prompt.get("text") if isinstance(prompt, dict) else ""
                created_at = prompt.get("created_at") if isinstance(prompt, dict) else None
                if not isinstance(text, str) or not text.strip():
                    continue
                received_at = float(created_at if isinstance(created_at, (int, float)) else time.time())
                source_event_id = f"prompt:{prompt_index}:{received_at}:{hashlib.sha256(text.encode('utf-8')).hexdigest()[:16]}"
                self._insert_message(connection, self._provider_message_row(
                    normalized_user, "codex", viewer_session_id, session.codex_session_id,
                    prompt_index, received_at, session.meta_path.as_posix(), source_event_id, None,
                    "user", "message:user", text.strip(), {"type": "codex_prompt", "index": prompt_index, "prompt": prompt},
                ))
            for compact in compact_events:
                raw_event = raw_by_index.get(compact.get("index"))
                raw = raw_event.get("raw") if isinstance(raw_event, dict) and isinstance(raw_event.get("raw"), dict) else compact.get("raw_preview") or {}
                index = int(compact.get("index") or 0)
                source_event_id = self._raw_identifier(raw) or f"{path or viewer_session_id}:{index + 1}"
                received_at = float(compact.get("received_at") or (raw_event.get("received_at") if isinstance(raw_event, dict) else time.time()))
                row = self._provider_message_row(
                    normalized_user, "codex", viewer_session_id, session.codex_session_id,
                    index, received_at, path, source_event_id, index + 1,
                    self._codex_role(raw, compact), str(compact.get("event_type") or AgentEventType.OPERATION),
                    str(compact.get("text") or ""), raw,
                )
                row["patch_text"] = compact.get("patch_text") if isinstance(compact.get("patch_text"), str) else None
                row["file_changes"] = compact.get("file_changes") or []
                self._insert_message(connection, row)
            connection.commit()

    def _sync_hermes_session(self, viewer_session_id: str, user_id: str | None) -> None:
        try:
            session = hermes_session_manager.get(viewer_session_id)
        except Exception:
            return
        normalized_user = normalize_user_id(user_id or session.user_id)
        if not session.hermes_session_id or not HERMES_STATE_DB.exists():
            return
        source: sqlite3.Connection | None = None
        try:
            source = sqlite3.connect(f"file:{HERMES_STATE_DB.as_posix()}?mode=ro", uri=True, timeout=1.0)
            source.row_factory = sqlite3.Row
            rows = source.execute(
                """
                SELECT id, role, content, tool_call_id, tool_calls, tool_name, timestamp,
                       token_count, finish_reason, reasoning, reasoning_content,
                       reasoning_details, codex_reasoning_items, codex_message_items
                FROM messages
                WHERE session_id = ?
                ORDER BY timestamp, id
                """,
                (session.hermes_session_id,),
            ).fetchall()
        except sqlite3.Error:
            return
        finally:
            if source is not None:
                source.close()
        with self.connect() as connection:
            event_index = 0
            for row in rows:
                raw = {key: row[key] for key in row.keys()}
                for event in self._hermes_events_from_row(row):
                    source_event_id = f"{row['id']}:{event['source']}"
                    self._insert_message(connection, self._provider_message_row(
                        normalized_user, "hermes", viewer_session_id, session.hermes_session_id,
                        event_index, float(row["timestamp"] or time.time()), HERMES_STATE_DB.as_posix(), source_event_id, None,
                        event["role"], event["event_type"], event["text"], raw,
                    ))
                    event_index += 1
            connection.commit()

    def _provider_message_row(
        self,
        user_id: str,
        provider: str,
        viewer_session_id: str,
        provider_session_id: str | None,
        event_index: int,
        received_at: float,
        source_path: str | None,
        source_event_id: str,
        source_line: int | None,
        role: str,
        event_type: str,
        text: str,
        raw: dict[str, Any],
    ) -> dict[str, Any]:
        return {
            "id": self._message_id(provider, viewer_session_id, source_event_id),
            "user_id": user_id,
            "conversation_id": "default",
            "parent_message_id": None,
            "sender_role_id": None,
            "recipient_role_id": None,
            "role_id": None,
            "query_message_id": None,
            "driver_run_id": None,
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
            "patch_text": None,
            "raw_json": self._json(raw),
            "occurred_at": received_at,
            "ingested_at": time.time(),
        }

    def _run_from_message(self, row: sqlite3.Row, message_limit: int = DEFAULT_MESSAGE_LIMIT) -> SuperHistoryRun:
        targets = [self._driver_from_row(target, message_limit=message_limit) for target in self._driver_rows(str(row["id"]))]
        selected_role_ids = [str(value) for value in self._parse_json(row["selected_role_ids_json"], []) if isinstance(value, str)]
        target_role_ids = [target.role_id for target in targets]
        query = str(row["query"] or "")
        return SuperHistoryRun(
            id=str(row["id"]),
            user_id=str(row["user_id"]),
            message=query,
            query=query,
            message_id=str(row["id"]),
            role_ids=target_role_ids or selected_role_ids,
            status=str(row["status"] or "queued"),
            rationale=str(row["rationale"] or ""),
            error=str(row["error"] or ""),
            parent_message_id=row["parent_message_id"] if isinstance(row["parent_message_id"], str) else None,
            sender_role_id=row["sender_role_id"] if isinstance(row["sender_role_id"], str) else None,
            created_at=float(row["occurred_at"]),
            updated_at=float(row["ingested_at"]),
            targets=targets,
        )

    def _driver_rows(self, query_message_id: str) -> list[sqlite3.Row]:
        with self.connect() as connection:
            return connection.execute(
                "SELECT * FROM super_workspace_driver_runs WHERE query_message_id = ? ORDER BY created_at, id",
                (query_message_id,),
            ).fetchall()

    def _driver_from_row(self, row: sqlite3.Row, message_limit: int = DEFAULT_MESSAGE_LIMIT) -> SuperHistoryTarget:
        provider = str(row["provider"])
        viewer_session_id = str(row["viewer_session_id"])
        self.sync_session(provider, viewer_session_id)
        messages = self._driver_messages(row, message_limit=message_limit)
        return SuperHistoryTarget(
            id=str(row["id"]),
            run_id=str(row["query_message_id"]),
            role_id=str(row["role_id"]),
            role_name=str(row["role_name"]),
            provider=provider,
            viewer_session_id=viewer_session_id,
            session_ref=str(row["session_ref"]),
            agent_prompt=str(row["agent_prompt"] or ""),
            status=str(row["status"]),
            created_at=float(row["created_at"]),
            updated_at=float(row["updated_at"]),
            messages=messages,
        )

    def _driver_messages(self, driver: sqlite3.Row, message_limit: int = DEFAULT_MESSAGE_LIMIT) -> list[AgentHistoryMessage]:
        provider = str(driver["provider"])
        viewer_session_id = str(driver["viewer_session_id"])
        driver_id = str(driver["id"])
        query_message_id = str(driver["query_message_id"])
        with self.connect() as connection:
            start = self._driver_anchor_time(connection, driver)
            if start is None:
                return []
            next_drivers = connection.execute(
                """
                SELECT * FROM super_workspace_driver_runs
                WHERE provider = ? AND viewer_session_id = ? AND id != ?
                ORDER BY created_at ASC, id ASC
                """,
                (provider, viewer_session_id, driver_id),
            ).fetchall()
            next_starts: list[float] = []
            for item in next_drivers:
                next_start = self._driver_anchor_time(connection, item)
                if next_start is not None and next_start > start:
                    next_starts.append(next_start)
            upper = "AND occurred_at < ?" if next_starts else ""
            upper_params = [min(next_starts)] if next_starts else []
            connection.execute(
                f"""
                UPDATE super_workspace_messages
                SET query_message_id = ?, driver_run_id = ?,
                    parent_message_id = ?, sender_role_id = ?, recipient_role_id = ?, role_id = ?
                WHERE provider = ? AND viewer_session_id = ? AND occurred_at >= ?
                  AND role != 'user'
                  {upper}
                """,
                [
                    query_message_id,
                    driver_id,
                    driver["parent_message_id"] if isinstance(driver["parent_message_id"], str) else None,
                    driver["sender_role_id"] if isinstance(driver["sender_role_id"], str) else None,
                    driver["recipient_role_id"] if isinstance(driver["recipient_role_id"], str) else str(driver["role_id"]),
                    str(driver["role_id"]),
                    provider,
                    viewer_session_id,
                    start,
                    *upper_params,
                ],
            )
            connection.commit()
            rows = connection.execute(
                f"""
                SELECT * FROM super_workspace_messages
                WHERE provider = ? AND viewer_session_id = ? AND occurred_at >= ?
                  AND role != 'user'
                  {upper}
                ORDER BY occurred_at ASC, id ASC
                LIMIT ?
                """,
                [provider, viewer_session_id, start, *upper_params, max(1, min(message_limit, 300))],
            ).fetchall()
        return [self._message_from_row(row) for row in rows]

    def _driver_anchor_time(self, connection: sqlite3.Connection, driver: sqlite3.Row) -> float | None:
        provider = str(driver["provider"])
        viewer_session_id = str(driver["viewer_session_id"])
        start = float(driver["start_after_occurred_at"] or driver["created_at"])
        prompt = str(driver["agent_prompt"] or "").strip()
        if not prompt:
            return start
        row = connection.execute(
            """
            SELECT occurred_at
            FROM super_workspace_messages
            WHERE provider = ? AND viewer_session_id = ? AND role = 'user'
              AND text = ? AND occurred_at >= ?
            ORDER BY occurred_at ASC, id ASC
            LIMIT 1
            """,
            (provider, viewer_session_id, prompt, start),
        ).fetchone()
        if row is None:
            return None
        return float(row["occurred_at"])

    def _message_from_row(self, row: sqlite3.Row) -> AgentHistoryMessage:
        query = row["query"] if isinstance(row["query"], str) and row["query"] else None
        query_message_id = row["query_message_id"] if isinstance(row["query_message_id"], str) else (str(row["id"]) if query else None)
        driver_run_id = row["driver_run_id"] if isinstance(row["driver_run_id"], str) else None
        return AgentHistoryMessage(
            id=str(row["id"]),
            provider=str(row["provider"]),
            viewer_session_id=row["viewer_session_id"] if isinstance(row["viewer_session_id"], str) else None,
            provider_session_id=row["provider_session_id"] if isinstance(row["provider_session_id"], str) else None,
            index=int(row["event_index"]),
            received_at=float(row["received_at"]),
            role=str(row["role"]),
            event_type=str(row["event_type"]),
            text=str(row["text"] or ""),
            query=query,
            status=row["status"] if isinstance(row["status"], str) else None,
            rationale=str(row["rationale"] or ""),
            error=str(row["error"] or ""),
            requested_role_ids=[str(value) for value in self._parse_json(row["requested_role_ids_json"], []) if isinstance(value, str)],
            selected_role_ids=[str(value) for value in self._parse_json(row["selected_role_ids_json"], []) if isinstance(value, str)],
            file_changes=self._file_changes_for_message(str(row["id"])),
            patch_text=row["patch_text"] if isinstance(row["patch_text"], str) else None,
            raw=self._parse_json(row["raw_json"], {}),
            occurred_at=float(row["occurred_at"]),
            query_id=query_message_id,
            query_message_id=query_message_id,
            driver_run_id=driver_run_id,
            super_run_id=query_message_id,
            super_target_id=driver_run_id,
            parent_message_id=row["parent_message_id"] if isinstance(row["parent_message_id"], str) else None,
            sender_role_id=row["sender_role_id"] if isinstance(row["sender_role_id"], str) else None,
            recipient_role_id=row["recipient_role_id"] if isinstance(row["recipient_role_id"], str) else None,
        )

    def _insert_message(self, connection: sqlite3.Connection, row: dict[str, Any]) -> None:
        file_changes = row.pop("file_changes", [])
        columns = ", ".join(row.keys())
        placeholders = ", ".join("?" for _ in row)
        updates = ", ".join(f"{key}=excluded.{key}" for key in row.keys() if key != "id")
        connection.execute(
            f"INSERT INTO super_workspace_messages ({columns}) VALUES ({placeholders}) ON CONFLICT(provider, viewer_session_id, source_event_id) DO UPDATE SET {updates}",
            tuple(row.values()),
        )
        self._replace_file_changes(connection, str(row["id"]), file_changes)

    def _replace_file_changes(self, connection: sqlite3.Connection, message_id: str, file_changes: Any) -> None:
        connection.execute("DELETE FROM super_workspace_message_file_changes WHERE message_id = ?", (message_id,))
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
            connection.execute(
                "INSERT INTO super_workspace_message_file_changes (message_id, position, path, change_type, diff) VALUES (?, ?, ?, ?, ?)",
                (message_id, position, path, change_type, diff),
            )

    def _file_changes_for_message(self, message_id: str) -> list[AgentHistoryFileChange]:
        with self.connect() as connection:
            rows = connection.execute(
                "SELECT path, change_type, diff FROM super_workspace_message_file_changes WHERE message_id = ? ORDER BY position",
                (message_id,),
            ).fetchall()
        return [AgentHistoryFileChange(path=str(row["path"]), change_type=str(row["change_type"]), diff=row["diff"] if isinstance(row["diff"], str) else None) for row in rows]

    def _latest_session_message(self, provider: str, viewer_session_id: str) -> sqlite3.Row | None:
        with self.connect() as connection:
            return connection.execute(
                "SELECT id, occurred_at FROM super_workspace_messages WHERE provider = ? AND viewer_session_id = ? ORDER BY occurred_at DESC, id DESC LIMIT 1",
                (provider, viewer_session_id),
            ).fetchone()

    def _provider_session_id(self, provider: str, viewer_session_id: str) -> str | None:
        try:
            if provider == "codex":
                return codex_session_manager.get(viewer_session_id).codex_session_id
            if provider == "hermes":
                return hermes_session_manager.get(viewer_session_id).hermes_session_id
        except Exception:
            return None
        return None

    def _hermes_events_from_row(self, row: sqlite3.Row) -> list[dict[str, str]]:
        role = str(row["role"] or "")
        events: list[dict[str, str]] = []
        if role == "user":
            text = row["content"] if isinstance(row["content"], str) else ""
            if text.strip():
                events.append({"source": "content", "role": "user", "event_type": "message:user", "text": text.strip()})
            return events
        reasoning = row["reasoning_content"] if isinstance(row["reasoning_content"], str) and row["reasoning_content"].strip() else row["reasoning"] if isinstance(row["reasoning"], str) else ""
        if reasoning.strip():
            events.append({"source": "reasoning", "role": "assistant", "event_type": AgentEventType.REASONING, "text": reasoning.strip()})
        content = row["content"] if isinstance(row["content"], str) else ""
        if role == "tool" and content.strip():
            tool_name = row["tool_name"] if isinstance(row["tool_name"], str) else ""
            prefix = f"Tool output: {tool_name}" if tool_name else "Tool output"
            events.append({"source": "tool_content", "role": "tool", "event_type": AgentEventType.TOOL_RESULT, "text": "\n".join(part for part in (prefix, content.strip()) if part)})
        elif content.strip():
            events.append({"source": "content", "role": role or "assistant", "event_type": "message:assistant" if role == "assistant" else f"message:{role}", "text": content.strip()})
        tool_calls = row["tool_calls"]
        if isinstance(tool_calls, str) and tool_calls.strip():
            events.append({"source": "tool_calls", "role": "assistant", "event_type": AgentEventType.TOOL_CALL, "text": tool_calls.strip()})
        return events

    def _parse_session_ref(self, session_ref: str, fallback_provider: str) -> tuple[str, str]:
        if ":" in session_ref:
            provider, session_id = session_ref.split(":", 1)
            return provider or fallback_provider, session_id
        return fallback_provider, session_ref

    def _codex_role(self, raw: dict, compact: dict) -> str:
        payload = raw.get("payload") if isinstance(raw.get("payload"), dict) else {}
        role = payload.get("role")
        if isinstance(role, str):
            return role
        event_type = str(compact.get("event_type") or "")
        if event_type.startswith("message:"):
            return event_type.split(":", 1)[1]
        if event_type in {AgentEventType.TOOL_RESULT, AgentEventType.TOOL_CALL, AgentEventType.FUNCTION_CALL, AgentEventType.CUSTOM_TOOL_CALL}:
            return "tool"
        return "assistant"

    def _raw_identifier(self, value: Any) -> str | None:
        if isinstance(value, dict):
            for key in ("id", "message_id", "item_id", "call_id", "turn_id"):
                item = value.get(key)
                if isinstance(item, str) and item:
                    return item
            found = self._raw_identifier(value.get("payload"))
            if found:
                return found
            return self._raw_identifier(value.get("item"))
        return None

    def _message_id(self, provider: str, viewer_session_id: str | None, source_event_id: str | None) -> str:
        value = f"{provider}:{viewer_session_id or ''}:{source_event_id or uuid.uuid4().hex}"
        return hashlib.sha256(value.encode("utf-8")).hexdigest()

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
