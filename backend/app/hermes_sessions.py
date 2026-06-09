import asyncio
from contextlib import suppress
import json
import os
import sqlite3
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from fastapi import HTTPException, WebSocket, WebSocketDisconnect
from loguru import logger

from .config import settings
from .files import resolve_served_directory
from .models import AgentEventType
from .storage import HERMES_LOG_DIR
from .users import default_user_id, normalize_user_id
from .ws_clients import WebSocketClient, add_client, enqueue, remove_client

HERMES_BASE_URL = os.environ.get("VIEWER_HERMES_BASE_URL", "http://127.0.0.1:8642").rstrip("/")
HERMES_API_KEY = os.environ.get("VIEWER_HERMES_API_KEY", "").strip()
HERMES_STATE_DB = Path(os.environ.get("VIEWER_HERMES_STATE_DB", "~/.hermes/state.db")).expanduser()
RAW_PREVIEW_MAX_BYTES = 16 * 1024
AGENT_DETAIL_FOCUS = "focus"
AGENT_DETAIL_FULL = "full"
AGENT_DETAILS = {AGENT_DETAIL_FOCUS, AGENT_DETAIL_FULL}
HERMES_SOURCE_EVENT_MAP = {
    "role:assistant:content": AgentEventType.MESSAGE_ASSISTANT,
    "role:assistant:reasoning": AgentEventType.REASONING,
    "role:assistant:reasoning_content": AgentEventType.REASONING,
    "role:assistant:tool_calls": AgentEventType.TOOL_CALL,
    "role:tool:content": AgentEventType.TOOL_RESULT,
}
HERMES_UNMAPPED_DB_FIELDS = ("reasoning_details", "codex_reasoning_items", "codex_message_items")


def normalize_agent_detail(detail: str | None) -> str:
    return detail if detail in AGENT_DETAILS else AGENT_DETAIL_FOCUS


def relative_cwd(cwd: str) -> str:
    path = Path(cwd)
    try:
        return path.relative_to(settings.root_resolved).as_posix()
    except ValueError:
        return path.as_posix()


@dataclass
class HermesSession:
    id: str
    user_id: str
    title: str
    cwd: str
    model: str | None
    created_at: float
    updated_at: float
    hermes_session_id: str | None = None
    hermes_run_id: str | None = None
    status: str = "idle"
    exit_code: int | None = None
    prompts: list[dict] = field(default_factory=list)
    events: list[dict] = field(default_factory=list)
    queue: list[dict] = field(default_factory=list)
    pending_approvals: list[dict] = field(default_factory=list)
    clients: dict[WebSocket, WebSocketClient] = field(default_factory=dict)
    run_task: asyncio.Task | None = None
    approval_stream_task: asyncio.Task | None = None
    meta_path: Path | None = None
    total_tokens: int | None = None
    suppress_queue_drain: bool = False
    local_approval_responses: int = 0
    lineage: dict[str, str | None] = field(default_factory=dict)

    def summary(self) -> dict:
        return {
            "id": self.id,
            "user_id": self.user_id,
            "hermes_session_id": self.hermes_session_id,
            "hermes_run_id": self.hermes_run_id,
            "db_path": HERMES_STATE_DB.as_posix(),
            "title": self.title,
            "cwd": self.cwd,
            "cwd_relative": relative_cwd(self.cwd),
            "model": self.model,
            "created_at": self.created_at,
            "updated_at": self.updated_at,
            "status": self.status,
            "exit_code": self.exit_code,
            "event_count": len(self.events),
            "total_tokens": self.total_tokens,
            "queue": self.queue,
            "pending_approvals": self.pending_approvals,
        }


class HermesSessionManager:
    def __init__(self) -> None:
        self.sessions: dict[str, HermesSession] = {}
        self._loaded = False
        self._logged_unmapped_fields: set[tuple[str, int | str, str]] = set()

    def _paths(self, session_id: str) -> Path:
        return HERMES_LOG_DIR / f"{session_id}.json"

    def _cwd_for(self, cwd: str | None, user_id: str | None) -> str:
        return resolve_served_directory(cwd, "Hermes", user_id)

    def _ensure_loaded(self) -> None:
        if self._loaded:
            return
        HERMES_LOG_DIR.mkdir(parents=True, exist_ok=True)
        for meta_path in sorted(HERMES_LOG_DIR.glob("*.json")):
            try:
                meta = json.loads(meta_path.read_text(encoding="utf-8"))
                session_id = str(meta["id"])
                session = HermesSession(
                    id=session_id,
                    user_id=meta.get("user_id") or default_user_id(),
                    hermes_session_id=meta.get("hermes_session_id"),
                    hermes_run_id=meta.get("hermes_run_id"),
                    title=meta.get("title") or "Hermes session",
                    cwd=meta.get("cwd") or settings.root_resolved.as_posix(),
                    model=meta.get("model"),
                    created_at=float(meta.get("created_at") or time.time()),
                    updated_at=float(meta.get("updated_at") or time.time()),
                    status=meta.get("status", "exited"),
                    exit_code=meta.get("exit_code"),
                    prompts=list(meta.get("prompts") or []),
                    queue=self._queue_from_meta(meta.get("queue")),
                    pending_approvals=self._approvals_from_meta(meta.get("pending_approvals")),
                    meta_path=meta_path,
                    lineage=meta.get("lineage") if isinstance(meta.get("lineage"), dict) else {},
                )
                self._sync_db_events(session)
                self.sessions[session_id] = session
            except Exception:
                logger.warning("Failed to load Hermes session metadata {}", meta_path)
        self._loaded = True

    def _queue_from_meta(self, value: Any) -> list[dict]:
        if not isinstance(value, list):
            return []
        queue: list[dict] = []
        for item in value:
            if not isinstance(item, dict):
                continue
            prompt = item.get("prompt")
            if not isinstance(prompt, str) or not prompt.strip():
                continue
            created_at = item.get("created_at")
            updated_at = item.get("updated_at")
            model = item.get("model")
            queue.append(
                {
                    "id": str(item.get("id") or uuid.uuid4().hex),
                    "prompt": prompt,
                    "created_at": float(created_at) if isinstance(created_at, (int, float)) else time.time(),
                    "updated_at": float(updated_at) if isinstance(updated_at, (int, float)) else time.time(),
                    "model": model if isinstance(model, str) and model.strip() else None,
                }
            )
        return queue

    def _approvals_from_meta(self, value: Any) -> list[dict]:
        if not isinstance(value, list):
            return []
        approvals: list[dict] = []
        for item in value:
            if not isinstance(item, dict):
                continue
            approval_id = item.get("id")
            if not isinstance(approval_id, str) or not approval_id:
                continue
            approvals.append(
                {
                    "id": approval_id,
                    "provider": "hermes",
                    "session_id": str(item.get("session_id") or ""),
                    "run_id": item.get("run_id") if isinstance(item.get("run_id"), str) else None,
                    "title": str(item.get("title") or "Approval required"),
                    "description": str(item.get("description") or ""),
                    "command": item.get("command") if isinstance(item.get("command"), str) else None,
                    "choices": item.get("choices") if isinstance(item.get("choices"), list) else ["once", "session", "always", "deny"],
                    "created_at": float(item.get("created_at") or time.time()),
                    "raw": item.get("raw") if isinstance(item.get("raw"), dict) else None,
                }
            )
        return approvals

    def _write_meta(self, session: HermesSession) -> None:
        if session.meta_path is None:
            return
        session.meta_path.parent.mkdir(parents=True, exist_ok=True)
        payload = json.dumps(
            {
                "id": session.id,
                "user_id": session.user_id,
                "hermes_session_id": session.hermes_session_id,
                "hermes_run_id": session.hermes_run_id,
                "title": session.title,
                "cwd": session.cwd,
                "model": session.model,
                "created_at": session.created_at,
                "updated_at": session.updated_at,
                "status": session.status,
                "exit_code": session.exit_code,
                "prompts": session.prompts,
                "queue": session.queue,
                "pending_approvals": session.pending_approvals,
                "lineage": session.lineage,
            },
            indent=2,
        )
        tmp_path = session.meta_path.with_suffix(f"{session.meta_path.suffix}.tmp")
        tmp_path.write_text(payload, encoding="utf-8")
        tmp_path.replace(session.meta_path)

    def _title_for(self, prompt: str) -> str:
        first = " ".join(prompt.strip().split())
        return first[:72] if first else "Hermes session"

    def _raw_preview(self, raw: dict) -> dict | None:
        encoded = json.dumps(raw, ensure_ascii=False, separators=(",", ":")).encode("utf-8", errors="replace")
        if len(encoded) <= RAW_PREVIEW_MAX_BYTES:
            return raw
        return {"type": raw.get("type"), "role": raw.get("role"), "omitted_bytes": len(encoded)}

    def _mapped_event_type(self, source: str, raw: dict) -> AgentEventType:
        event_type = HERMES_SOURCE_EVENT_MAP.get(source)
        if event_type:
            return event_type
        logger.warning(
            "Unmapped Hermes event source source={} row_id={} role={} preview={}",
            source,
            raw.get("id"),
            raw.get("role"),
            self._preview_text(raw),
        )
        return AgentEventType.OPERATION

    def _preview_text(self, value: Any, limit: int = 1200) -> str:
        if isinstance(value, str):
            text = value
        else:
            try:
                text = json.dumps(value, ensure_ascii=False, default=str)
            except TypeError:
                text = str(value)
        text = text.replace("\r\n", "\n")
        return text if len(text) <= limit else f"{text[:limit]}...<truncated>"

    def _log_unmapped_field(self, session: HermesSession, row: sqlite3.Row, field: str, value: Any) -> None:
        row_id = row["id"] if "id" in row.keys() else "unknown"
        key = (session.hermes_session_id or session.id, row_id, field)
        if key in self._logged_unmapped_fields:
            return
        self._logged_unmapped_fields.add(key)
        logger.warning(
            "Unmapped Hermes message field session={} row_id={} role={} field={} preview={}",
            session.hermes_session_id or session.id,
            row_id,
            row["role"] if "role" in row.keys() else None,
            field,
            self._preview_text(value),
        )

    def _add_event(self, events: list[dict], timestamp: float, event_type: AgentEventType, text: str, raw: dict) -> None:
        cleaned = text.strip()
        if not cleaned:
            return
        events.append(
            {
                "index": len(events),
                "received_at": timestamp,
                "event_type": event_type,
                "text": cleaned,
                "file_changes": [],
                "patch_text": None,
                "raw_preview": self._raw_preview(raw),
            }
        )

    def _add_mapped_event(self, events: list[dict], timestamp: float, source: str, text: str, raw: dict) -> None:
        if not text.strip():
            return
        self._add_event(events, timestamp, self._mapped_event_type(source, raw), text, raw)

    def _tool_call_text(self, row: sqlite3.Row) -> str:
        tool_calls = row["tool_calls"] if isinstance(row["tool_calls"], str) else ""
        tool_name = row["tool_name"] if isinstance(row["tool_name"], str) else ""
        if not tool_calls:
            return ""
        try:
            parsed = json.loads(tool_calls)
        except json.JSONDecodeError:
            return f"Tool call: {tool_name}" if tool_name else "Tool call"
        calls = parsed if isinstance(parsed, list) else [parsed]
        rows: list[str] = []
        for call in calls:
            if not isinstance(call, dict):
                continue
            function = call.get("function")
            name = ""
            arguments = ""
            if isinstance(function, dict):
                name = function.get("name") if isinstance(function.get("name"), str) else ""
                arguments = function.get("arguments") if isinstance(function.get("arguments"), str) else ""
            if not name:
                name = call.get("name") if isinstance(call.get("name"), str) else tool_name
            label = f"Tool call: {name}" if name else "Tool call"
            rows.append("\n".join(part for part in (label, arguments.strip()) if part))
        return "\n\n".join(rows) if rows else (f"Tool call: {tool_name}" if tool_name else "Tool call")

    def _message_text(self, row: sqlite3.Row) -> str:
        role = str(row["role"] or "")
        content = row["content"] if isinstance(row["content"], str) else ""
        if role == "tool":
            tool_name = row["tool_name"] if isinstance(row["tool_name"], str) else ""
            prefix = f"Tool output: {tool_name}" if tool_name else "Tool output"
            return "\n".join(part for part in (prefix, content) if part)
        return content

    def _events_from_db(self, session: HermesSession) -> tuple[list[dict], int | None, float | None]:
        if not session.hermes_session_id or not HERMES_STATE_DB.exists():
            return [], None, None
        try:
            connection = sqlite3.connect(f"file:{HERMES_STATE_DB.as_posix()}?mode=ro", uri=True, timeout=1.0)
            connection.row_factory = sqlite3.Row
            try:
                rows = connection.execute(
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
            finally:
                connection.close()
        except sqlite3.Error:
            logger.debug("Failed to read Hermes DB {}", HERMES_STATE_DB)
            return [], None, None

        events: list[dict] = []
        total_tokens = 0
        last_ts: float | None = None
        for row in rows:
            role = str(row["role"] or "")
            token_count = row["token_count"]
            if isinstance(token_count, int):
                total_tokens += token_count
            timestamp = float(row["timestamp"] or time.time())
            last_ts = max(last_ts or timestamp, timestamp)
            if role == "user":
                continue
            raw = {key: row[key] for key in row.keys()}
            for field in HERMES_UNMAPPED_DB_FIELDS:
                if isinstance(row[field], str) and row[field].strip():
                    self._log_unmapped_field(session, row, field, row[field])
            reasoning_source = "role:assistant:reasoning_content" if isinstance(row["reasoning_content"], str) and row["reasoning_content"].strip() else "role:assistant:reasoning"
            reasoning = row["reasoning_content"] if reasoning_source.endswith("reasoning_content") else row["reasoning"] if isinstance(row["reasoning"], str) else ""
            self._add_mapped_event(events, timestamp, reasoning_source, reasoning, raw)
            message_text = self._message_text(row)
            self._add_mapped_event(events, timestamp, f"role:{role}:content", message_text, raw)
            self._add_mapped_event(events, timestamp, f"role:{role}:tool_calls", self._tool_call_text(row), raw)
        return events, total_tokens or None, last_ts

    def _sync_db_events(self, session: HermesSession) -> list[dict]:
        events, total_tokens, last_ts = self._events_from_db(session)
        old_count = len(session.events)
        session.events = events
        if total_tokens is not None:
            session.total_tokens = total_tokens
        if last_ts is not None:
            session.updated_at = max(session.updated_at, last_ts)
        if session.hermes_session_id and HERMES_STATE_DB.exists():
            self._sync_session_row(session)
        new_events = events[old_count:]
        if new_events and session.lineage:
            self._record_lineage_events(session, new_events, old_count)
        if new_events:
            self._write_meta(session)
        return new_events

    def _record_lineage_events(self, session: HermesSession, events: list[dict], start_index: int) -> None:
        try:
            from .agent_history import agent_history_store
        except Exception:
            return
        lineage = session.lineage
        for offset, event in enumerate(events):
            absolute_index = start_index + offset
            raw = event.get("raw_preview") if isinstance(event.get("raw_preview"), dict) else {"source": "hermes"}
            event_type = event.get("event_type")
            agent_history_store.record_provider_message(
                user_id=session.user_id,
                workspace_id=lineage.get("workspace_id"),
                provider="hermes",
                viewer_session_id=session.id,
                provider_session_id=session.hermes_session_id,
                query_message_id=lineage.get("query_message_id"),
                driver_run_id=lineage.get("driver_run_id"),
                parent_message_id=lineage.get("parent_message_id"),
                sender_role_id=lineage.get("sender_role_id"),
                recipient_role_id=lineage.get("recipient_role_id"),
                role_id=lineage.get("role_id"),
                event_index=absolute_index,
                received_at=float(event.get("received_at") or time.time()),
                source_path=HERMES_STATE_DB.as_posix(),
                source_event_id=f"hermes:{session.hermes_session_id}:{absolute_index}",
                source_line=None,
                role="assistant",
                event_type=str(event_type or "operation"),
                text=str(event.get("text") or ""),
                raw=raw,
            )

    def _sync_session_row(self, session: HermesSession) -> None:
        try:
            connection = sqlite3.connect(f"file:{HERMES_STATE_DB.as_posix()}?mode=ro", uri=True, timeout=1.0)
            connection.row_factory = sqlite3.Row
            try:
                row = connection.execute(
                    "SELECT title, model, ended_at, end_reason, message_count, input_tokens, output_tokens FROM sessions WHERE id = ?",
                    (session.hermes_session_id,),
                ).fetchone()
            finally:
                connection.close()
        except sqlite3.Error:
            return
        if not row:
            return
        if isinstance(row["title"], str) and row["title"].strip():
            session.title = row["title"].strip()
        if isinstance(row["model"], str) and row["model"].strip():
            session.model = row["model"].strip()
        if row["ended_at"] and session.status == "running":
            session.status = "exited"
            session.exit_code = 0
        input_tokens = row["input_tokens"] if isinstance(row["input_tokens"], int) else 0
        output_tokens = row["output_tokens"] if isinstance(row["output_tokens"], int) else 0
        if input_tokens or output_tokens:
            session.total_tokens = input_tokens + output_tokens

    def _event_for_detail(self, event: dict, detail: str) -> dict | None:
        if detail == AGENT_DETAIL_FULL:
            return event
        if event.get("event_type") != AgentEventType.MESSAGE_ASSISTANT:
            return None
        return {
            **event,
            "file_changes": [],
            "patch_text": None,
            "raw_preview": None,
        }

    def _events_for_detail(self, events: list[dict], detail: str) -> list[dict]:
        normalized = normalize_agent_detail(detail)
        rows: list[dict] = []
        for event in events:
            next_event = self._event_for_detail(event, normalized)
            if next_event is not None:
                rows.append(next_event)
        return rows

    def _snapshot(self, session: HermesSession, detail: str | None = None) -> dict:
        self._sync_db_events(session)
        return {**session.summary(), "prompts": session.prompts, "events": self._events_for_detail(session.events, normalize_agent_detail(detail))}

    def snapshot(self, session_id: str, detail: str | None = None) -> dict:
        return self._snapshot(self.get(session_id), detail)

    def list(self, user_id: str | None = None) -> list[dict]:
        self._ensure_loaded()
        normalized_user_id = normalize_user_id(user_id)
        for session in self.sessions.values():
            if session.status == "running":
                self._sync_db_events(session)
        return sorted(
            (session.summary() for session in self.sessions.values() if session.user_id == normalized_user_id),
            key=lambda item: item["updated_at"],
            reverse=True,
        )

    def get(self, session_id: str) -> HermesSession:
        self._ensure_loaded()
        session = self.sessions.get(session_id)
        if not session:
            raise HTTPException(status_code=404, detail="Hermes session not found")
        self._sync_db_events(session)
        return session

    async def create(
        self,
        prompt: str,
        cwd: str | None = None,
        model: str | None = None,
        user_id: str | None = None,
        lineage: dict[str, str | None] | None = None,
    ) -> dict:
        self._ensure_loaded()
        session_id = uuid.uuid4().hex
        normalized_user_id = normalize_user_id(user_id)
        now = time.time()
        cleaned_prompt = prompt.strip()
        session = HermesSession(
            id=session_id,
            user_id=normalized_user_id,
            hermes_session_id=session_id,
            title=self._title_for(cleaned_prompt) if cleaned_prompt else "New Hermes session",
            cwd=self._cwd_for(cwd, normalized_user_id),
            model=model.strip() if isinstance(model, str) and model.strip() else None,
            created_at=now,
            updated_at=now,
            status="idle",
            prompts=[{"text": cleaned_prompt, "created_at": now}] if cleaned_prompt else [],
            meta_path=self._paths(session_id),
            lineage=dict(lineage or {}),
        )
        self.sessions[session_id] = session
        self._write_meta(session)
        if cleaned_prompt:
            session.status = "running"
            session.run_task = asyncio.create_task(self._run(session, cleaned_prompt))
        return session.summary()

    async def send(self, session_id: str, prompt: str, model: str | None = None, lineage: dict[str, str | None] | None = None) -> dict:
        session = self.get(session_id)
        cleaned_prompt = prompt.strip()
        if not cleaned_prompt:
            raise HTTPException(status_code=400, detail="Prompt is required")
        if session.status == "running":
            raise HTTPException(status_code=409, detail="Hermes session is already running")
        now = time.time()
        session.prompts.append({"text": cleaned_prompt, "created_at": now})
        if model:
            session.model = model
        if lineage is not None:
            session.lineage = dict(lineage)
        if not session.hermes_session_id:
            session.hermes_session_id = session.id
        if not session.events:
            session.title = self._title_for(cleaned_prompt)
        session.status = "running"
        session.exit_code = None
        session.updated_at = now
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
        session.run_task = asyncio.create_task(self._run(session, cleaned_prompt))
        return session.summary()

    async def enqueue(self, session_id: str, prompt: str, model: str | None = None) -> dict:
        session = self.get(session_id)
        cleaned_prompt = prompt.strip()
        if not cleaned_prompt:
            raise HTTPException(status_code=400, detail="Prompt is required")
        now = time.time()
        session.queue.append(
            {
                "id": uuid.uuid4().hex,
                "prompt": cleaned_prompt,
                "created_at": now,
                "updated_at": now,
                "model": model.strip() if isinstance(model, str) and model.strip() else None,
            }
        )
        session.updated_at = now
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
        if session.status != "running":
            await self._start_next_queued(session)
        return session.summary()

    async def update_queue_item(self, session_id: str, item_id: str, prompt: str, model: str | None = None) -> dict:
        session = self.get(session_id)
        cleaned_prompt = prompt.strip()
        if not cleaned_prompt:
            raise HTTPException(status_code=400, detail="Prompt is required")
        for item in session.queue:
            if item.get("id") != item_id:
                continue
            item["prompt"] = cleaned_prompt
            item["updated_at"] = time.time()
            item["model"] = model.strip() if isinstance(model, str) and model.strip() else item.get("model")
            session.updated_at = time.time()
            self._write_meta(session)
            await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
            return session.summary()
        raise HTTPException(status_code=404, detail="Queued message not found")

    async def delete_queue_item(self, session_id: str, item_id: str) -> dict:
        session = self.get(session_id)
        original_count = len(session.queue)
        session.queue = [item for item in session.queue if item.get("id") != item_id]
        if len(session.queue) == original_count:
            raise HTTPException(status_code=404, detail="Queued message not found")
        session.updated_at = time.time()
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
        return session.summary()

    async def _start_next_queued(self, session: HermesSession) -> bool:
        if session.status == "running" or not session.queue:
            return False
        item = session.queue.pop(0)
        prompt = str(item.get("prompt") or "").strip()
        if not prompt:
            self._write_meta(session)
            await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
            return await self._start_next_queued(session)
        model = item.get("model")
        if isinstance(model, str) and model.strip():
            session.model = model.strip()
        now = time.time()
        session.prompts.append({"text": prompt, "created_at": now})
        if not session.events:
            session.title = self._title_for(prompt)
        session.status = "running"
        session.exit_code = None
        session.updated_at = now
        session.suppress_queue_drain = False
        self._write_meta(session)
        await self._broadcast(session, {"type": "snapshot", "session": self._snapshot(session, AGENT_DETAIL_FULL), "source": "hermes"})
        session.run_task = asyncio.create_task(self._run(session, prompt))
        return True

    def _request_json(self, method: str, path: str, payload: dict | None = None, timeout: float = 10.0) -> dict:
        body = json.dumps(payload).encode("utf-8") if payload is not None else None
        headers = {"Content-Type": "application/json"}
        if HERMES_API_KEY:
            headers["Authorization"] = f"Bearer {HERMES_API_KEY}"
        request = Request(f"{HERMES_BASE_URL}{path}", data=body, headers=headers, method=method)
        try:
            with urlopen(request, timeout=timeout) as response:
                data = response.read().decode("utf-8", errors="replace")
        except HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace") or str(exc)
            raise HTTPException(status_code=exc.code, detail=detail) from exc
        except URLError as exc:
            raise HTTPException(status_code=503, detail=f"Hermes API server is unavailable at {HERMES_BASE_URL}: {exc.reason}") from exc
        except TimeoutError as exc:
            raise HTTPException(status_code=503, detail=f"Hermes API server timed out at {HERMES_BASE_URL}") from exc
        try:
            value = json.loads(data)
        except json.JSONDecodeError as exc:
            raise HTTPException(status_code=502, detail="Hermes API server returned invalid JSON") from exc
        return value if isinstance(value, dict) else {}

    def _request_stream(self, path: str, timeout: float = 5.0):
        headers = {"Accept": "text/event-stream"}
        if HERMES_API_KEY:
            headers["Authorization"] = f"Bearer {HERMES_API_KEY}"
        request = Request(f"{HERMES_BASE_URL}{path}", headers=headers, method="GET")
        return urlopen(request, timeout=timeout)

    def _run_payload(self, session: HermesSession, prompt: str) -> dict:
        payload: dict[str, Any] = {
            "input": prompt,
            "session_id": session.hermes_session_id or session.id,
            "instructions": f"Working directory: {session.cwd}",
        }
        if session.model:
            payload["model"] = session.model
        return payload

    async def _run(self, session: HermesSession, prompt: str) -> None:
        try:
            response = await asyncio.to_thread(self._request_json, "POST", "/v1/runs", self._run_payload(session, prompt), 30.0)
            run_id = response.get("id") or response.get("run_id")
            if isinstance(run_id, str) and run_id:
                session.hermes_run_id = run_id
                self._start_approval_stream(session, run_id)
            hermes_session_id = response.get("session_id")
            if isinstance(hermes_session_id, str) and hermes_session_id:
                session.hermes_session_id = hermes_session_id
            self._write_meta(session)
            response_status = response.get("status")
            if isinstance(response_status, str) and response_status in {"completed", "succeeded", "exited", "failed", "cancelled", "canceled"}:
                session.status = "failed" if response_status in {"failed", "cancelled", "canceled"} else "exited"
                session.exit_code = 1 if session.status == "failed" else 0
                self._sync_db_events(session)
                session.updated_at = time.time()
                self._write_meta(session)
                await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
            else:
                await self._monitor_run(session)
        except HTTPException as exc:
            session.status = "failed"
            session.exit_code = exc.status_code
            session.updated_at = time.time()
            self._write_meta(session)
            await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
            logger.warning("Hermes run failed session={} detail={}", session.id, exc.detail)
        except asyncio.CancelledError:
            raise
        except Exception:
            session.status = "failed"
            session.exit_code = 1
            session.updated_at = time.time()
            self._write_meta(session)
            await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
            logger.exception("Hermes run failed session={}", session.id)
        finally:
            session.run_task = None
            if session.approval_stream_task:
                session.approval_stream_task.cancel()
                with suppress(asyncio.CancelledError):
                    await session.approval_stream_task
                session.approval_stream_task = None
            if session.status != "running" and not session.suppress_queue_drain:
                await self._start_next_queued(session)

    def _start_approval_stream(self, session: HermesSession, run_id: str) -> None:
        if session.approval_stream_task and not session.approval_stream_task.done():
            session.approval_stream_task.cancel()
        session.approval_stream_task = asyncio.create_task(self._stream_run_events(session.id, run_id))

    async def _stream_run_events(self, session_id: str, run_id: str) -> None:
        loop = asyncio.get_running_loop()
        try:
            await asyncio.to_thread(self._consume_run_events, session_id, run_id, loop)
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            logger.debug("Hermes run event stream ended session={} run={} error={}", session_id, run_id, exc)

    def _consume_run_events(self, session_id: str, run_id: str, loop: asyncio.AbstractEventLoop) -> None:
        data_lines: list[str] = []
        with self._request_stream(f"/v1/runs/{run_id}/events", 5.0) as response:
            for raw_line in response:
                line = raw_line.decode("utf-8", errors="replace").rstrip("\r\n")
                if not line:
                    if data_lines:
                        payload = "\n".join(data_lines)
                        data_lines = []
                        try:
                            event = json.loads(payload)
                        except json.JSONDecodeError:
                            continue
                        loop.call_soon_threadsafe(lambda event=event: asyncio.create_task(self._handle_run_event(session_id, event)))
                    continue
                if line.startswith("data:"):
                    data_lines.append(line[5:].lstrip())

    async def _handle_run_event(self, session_id: str, event: dict) -> None:
        session = self.sessions.get(session_id)
        if not session:
            return
        event_name = event.get("event")
        if event_name == "approval.request":
            approval = self._approval_from_event(session, event)
            if not any(item.get("id") == approval["id"] for item in session.pending_approvals):
                session.pending_approvals.append(approval)
            session.updated_at = time.time()
            self._write_meta(session)
            await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
            return
        if event_name == "approval.responded":
            resolved = event.get("resolved")
            count = resolved if isinstance(resolved, int) and resolved > 0 else 1
            local_count = min(session.local_approval_responses, count)
            session.local_approval_responses -= local_count
            external_count = count - local_count
            if external_count > 0:
                session.pending_approvals = session.pending_approvals[external_count:]
            session.updated_at = time.time()
            self._write_meta(session)
            await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})

    def _approval_from_event(self, session: HermesSession, event: dict) -> dict:
        run_id = event.get("run_id") if isinstance(event.get("run_id"), str) else session.hermes_run_id
        timestamp = float(event.get("timestamp") or time.time())
        command = event.get("command") if isinstance(event.get("command"), str) else None
        description = event.get("description") if isinstance(event.get("description"), str) else ""
        approval_id = f"{run_id or session.id}:{len(session.pending_approvals) + 1}:{int(timestamp * 1000)}"
        choices = event.get("choices") if isinstance(event.get("choices"), list) else ["once", "session", "always", "deny"]
        return {
            "id": approval_id,
            "provider": "hermes",
            "session_id": session.id,
            "run_id": run_id,
            "title": "Command approval required",
            "description": description,
            "command": command,
            "choices": [str(choice) for choice in choices if str(choice)],
            "created_at": timestamp,
            "raw": event,
        }

    async def _monitor_run(self, session: HermesSession) -> None:
        idle_polls = 0
        while session.status == "running":
            new_events = self._sync_db_events(session)
            for event in new_events:
                await self._broadcast(session, {"type": "event", "event": event, "session": session.summary(), "source": "hermes"})
            if session.hermes_run_id:
                status = await asyncio.to_thread(self._read_run_status, session.hermes_run_id)
                if status in {"completed", "succeeded", "exited", "failed", "cancelled", "canceled"}:
                    session.status = "failed" if status in {"failed", "cancelled", "canceled"} else "exited"
                    session.exit_code = 1 if session.status == "failed" else 0
                    break
            idle_polls += 1
            if idle_polls > 1800:
                session.status = "failed"
                session.exit_code = -1
                break
            await asyncio.sleep(1.0)
        self._sync_db_events(session)
        session.updated_at = time.time()
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})

    def _read_run_status(self, run_id: str) -> str | None:
        try:
            response = self._request_json("GET", f"/v1/runs/{run_id}", None, 5.0)
        except HTTPException:
            return None
        status = response.get("status")
        return status if isinstance(status, str) else None

    async def resolve_approval(self, session_id: str, approval_id: str, choice: str, resolve_all: bool = False) -> dict:
        session = self.get(session_id)
        approval = next((item for item in session.pending_approvals if item.get("id") == approval_id), None)
        if not approval:
            raise HTTPException(status_code=404, detail="Pending approval not found")
        run_id = approval.get("run_id") or session.hermes_run_id
        if not isinstance(run_id, str) or not run_id:
            raise HTTPException(status_code=409, detail="Approval has no Hermes run id")
        normalized = {"approve": "once", "allow": "once"}.get(choice, choice)
        response = await asyncio.to_thread(
            self._request_json,
            "POST",
            f"/v1/runs/{run_id}/approval",
            {"choice": normalized, "all": resolve_all},
            10.0,
        )
        resolved_count = response.get("resolved")
        session.local_approval_responses += resolved_count if isinstance(resolved_count, int) and resolved_count > 0 else 1
        if resolve_all:
            session.pending_approvals = []
        else:
            session.pending_approvals = [item for item in session.pending_approvals if item.get("id") != approval_id]
        session.updated_at = time.time()
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
        return {**session.summary(), "approval_response": response}

    async def terminate(self, session_id: str) -> dict:
        session = self.get(session_id)
        session.suppress_queue_drain = True
        if session.hermes_run_id:
            with suppress(HTTPException):
                await asyncio.to_thread(self._request_json, "POST", f"/v1/runs/{session.hermes_run_id}/stop", {}, 10.0)
        if session.run_task:
            session.run_task.cancel()
            with suppress(asyncio.CancelledError):
                await session.run_task
            session.run_task = None
        if session.approval_stream_task:
            session.approval_stream_task.cancel()
            with suppress(asyncio.CancelledError):
                await session.approval_stream_task
            session.approval_stream_task = None
        if session.status == "running":
            session.status = "exited"
        session.updated_at = time.time()
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
        return session.summary()

    async def connect(self, session_id: str, websocket: WebSocket, detail: str | None = None) -> None:
        session = self.get(session_id)
        normalized_detail = normalize_agent_detail(detail)
        await websocket.accept()
        client = add_client(session.clients, websocket)
        setattr(client, "agent_detail", normalized_detail)
        enqueue(client, {"type": "snapshot", "session": self._snapshot(session, normalized_detail), "source": "hermes"})
        try:
            while True:
                await websocket.receive_text()
        except WebSocketDisconnect:
            pass
        except RuntimeError as exc:
            if "disconnect" not in str(exc).lower():
                raise
        finally:
            await self._remove_client(session, client)

    async def _broadcast(self, session: HermesSession, message: dict) -> None:
        stale: list[WebSocketClient] = []
        for client in list(session.clients.values()):
            if client.writer_task and client.writer_task.done():
                stale.append(client)
                continue
            detail = normalize_agent_detail(getattr(client, "agent_detail", AGENT_DETAIL_FOCUS))
            next_message = self._message_for_detail(session, message, detail)
            if next_message is None:
                continue
            if not enqueue(client, next_message):
                stale.append(client)
        for client in stale:
            await remove_client(session.clients, client)

    def _message_for_detail(self, session: HermesSession, message: dict, detail: str) -> dict | None:
        message_type = message.get("type")
        if message_type == "snapshot":
            return {**message, "session": self._snapshot(session, detail)}
        if message_type == "event":
            event = message.get("event")
            if not isinstance(event, dict):
                return message
            next_event = self._event_for_detail(event, detail)
            if next_event is None:
                return None
            return {**message, "event": next_event}
        return message

    async def _remove_client(self, session: HermesSession, client: WebSocketClient) -> None:
        await remove_client(session.clients, client)

    async def resume_pending_queues(self) -> None:
        self._ensure_loaded()
        for session in list(self.sessions.values()):
            if session.status == "running":
                if session.hermes_run_id:
                    self._start_approval_stream(session, session.hermes_run_id)
                session.run_task = asyncio.create_task(self._monitor_run(session))
                continue
            if session.queue:
                await self._start_next_queued(session)

    async def shutdown(self) -> None:
        for session in list(self.sessions.values()):
            if session.run_task:
                session.run_task.cancel()
            if session.approval_stream_task:
                session.approval_stream_task.cancel()
        for session in list(self.sessions.values()):
            if session.run_task:
                with suppress(asyncio.CancelledError):
                    await session.run_task
            if session.approval_stream_task:
                with suppress(asyncio.CancelledError):
                    await session.approval_stream_task


hermes_session_manager = HermesSessionManager()
