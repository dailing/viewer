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
from .ws_clients import WebSocketClient, add_client, broadcast, enqueue, remove_client

HERMES_BASE_URL = os.environ.get("VIEWER_HERMES_BASE_URL", "http://127.0.0.1:8642").rstrip("/")
HERMES_API_KEY = os.environ.get("VIEWER_HERMES_API_KEY", "").strip()
HERMES_STATE_DB = Path(os.environ.get("VIEWER_HERMES_STATE_DB", "~/.hermes/state.db")).expanduser()
RAW_PREVIEW_MAX_BYTES = 16 * 1024
HERMES_SOURCE_EVENT_MAP = {
    "role:assistant:content": AgentEventType.MESSAGE_ASSISTANT,
    "role:assistant:reasoning": AgentEventType.REASONING,
    "role:assistant:reasoning_content": AgentEventType.REASONING,
    "role:assistant:tool_calls": AgentEventType.TOOL_CALL,
    "role:tool:content": AgentEventType.TOOL_RESULT,
}
HERMES_UNMAPPED_DB_FIELDS = ("reasoning_details", "codex_reasoning_items", "codex_message_items")


def relative_cwd(cwd: str) -> str:
    path = Path(cwd)
    try:
        return path.relative_to(settings.root_resolved).as_posix()
    except ValueError:
        return path.as_posix()


@dataclass
class HermesSession:
    id: str
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
    clients: dict[WebSocket, WebSocketClient] = field(default_factory=dict)
    run_task: asyncio.Task | None = None
    meta_path: Path | None = None
    total_tokens: int | None = None
    suppress_queue_drain: bool = False

    def summary(self) -> dict:
        return {
            "id": self.id,
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
        }


class HermesSessionManager:
    def __init__(self) -> None:
        self.sessions: dict[str, HermesSession] = {}
        self._loaded = False
        self._logged_unmapped_fields: set[tuple[str, int | str, str]] = set()

    def _paths(self, session_id: str) -> Path:
        return HERMES_LOG_DIR / f"{session_id}.json"

    def _cwd_for(self, cwd: str | None) -> str:
        return resolve_served_directory(cwd, "Hermes")

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
                    meta_path=meta_path,
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

    def _write_meta(self, session: HermesSession) -> None:
        if session.meta_path is None:
            return
        session.meta_path.parent.mkdir(parents=True, exist_ok=True)
        payload = json.dumps(
            {
                "id": session.id,
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
        if new_events:
            self._write_meta(session)
        return new_events

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

    def _snapshot(self, session: HermesSession) -> dict:
        self._sync_db_events(session)
        return {**session.summary(), "prompts": session.prompts, "events": session.events}

    def snapshot(self, session_id: str) -> dict:
        return self._snapshot(self.get(session_id))

    def list(self) -> list[dict]:
        self._ensure_loaded()
        for session in self.sessions.values():
            self._sync_db_events(session)
        return sorted((session.summary() for session in self.sessions.values()), key=lambda item: item["updated_at"], reverse=True)

    def get(self, session_id: str) -> HermesSession:
        self._ensure_loaded()
        session = self.sessions.get(session_id)
        if not session:
            raise HTTPException(status_code=404, detail="Hermes session not found")
        self._sync_db_events(session)
        return session

    async def create(self, prompt: str, cwd: str | None = None, model: str | None = None) -> dict:
        self._ensure_loaded()
        session_id = uuid.uuid4().hex
        now = time.time()
        cleaned_prompt = prompt.strip()
        session = HermesSession(
            id=session_id,
            hermes_session_id=session_id,
            title=self._title_for(cleaned_prompt) if cleaned_prompt else "New Hermes session",
            cwd=self._cwd_for(cwd),
            model=model.strip() if isinstance(model, str) and model.strip() else None,
            created_at=now,
            updated_at=now,
            status="idle",
            prompts=[{"text": cleaned_prompt, "created_at": now}] if cleaned_prompt else [],
            meta_path=self._paths(session_id),
        )
        self.sessions[session_id] = session
        self._write_meta(session)
        if cleaned_prompt:
            session.status = "running"
            session.run_task = asyncio.create_task(self._run(session, cleaned_prompt))
        return session.summary()

    async def send(self, session_id: str, prompt: str, model: str | None = None) -> dict:
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
        await self._broadcast(session, {"type": "snapshot", "session": self._snapshot(session), "source": "hermes"})
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
            if session.status != "running" and not session.suppress_queue_drain:
                await self._start_next_queued(session)

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
        if session.status == "running":
            session.status = "exited"
        session.updated_at = time.time()
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary(), "source": "hermes"})
        return session.summary()

    async def connect(self, session_id: str, websocket: WebSocket) -> None:
        session = self.get(session_id)
        await websocket.accept()
        client = add_client(session.clients, websocket)
        enqueue(client, {"type": "snapshot", "session": self._snapshot(session), "source": "hermes"})
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
        await broadcast(session.clients, message)

    async def _remove_client(self, session: HermesSession, client: WebSocketClient) -> None:
        await remove_client(session.clients, client)

    async def resume_pending_queues(self) -> None:
        self._ensure_loaded()
        for session in list(self.sessions.values()):
            if session.status == "running":
                session.run_task = asyncio.create_task(self._monitor_run(session))
                continue
            if session.queue:
                await self._start_next_queued(session)

    async def shutdown(self) -> None:
        for session in list(self.sessions.values()):
            if session.run_task:
                session.run_task.cancel()
        for session in list(self.sessions.values()):
            if session.run_task:
                with suppress(asyncio.CancelledError):
                    await session.run_task


hermes_session_manager = HermesSessionManager()
