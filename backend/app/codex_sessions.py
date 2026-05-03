import asyncio
from collections import deque
from contextlib import suppress
from datetime import datetime, timezone
import json
import os
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from fastapi import HTTPException, WebSocket, WebSocketDisconnect
from loguru import logger
from watchfiles import awatch

from .config import settings
from .files import resolve_served_directory
from .ws_clients import WebSocketClient, add_client, broadcast, enqueue, remove_client

CODEX_LOG_DIR = Path(__file__).resolve().parents[2] / "logs" / "codex-sessions"
CODEX_ROLLOUT_ROOT = Path.home() / ".codex" / "sessions"
PROXY_ENV_KEYS = ("https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY")
RAW_PREVIEW_MAX_BYTES = 16 * 1024
HIDDEN_DISPLAY_EVENT_TYPES = {
    "session_meta",
    "turn_context",
    "task_started",
    "task_complete",
    "turn_aborted",
    "context_compacted",
    "token_count",
    "user_message",
    "turn.started",
    "turn.completed",
    "thread.started",
    "message:developer",
    "message:system",
    "message:user",
    "function_call_output",
    "custom_tool_call_output",
    "web_search_call",
    "web_search_end",
}


def relative_cwd(cwd: str) -> str:
    path = Path(cwd)
    try:
        return path.relative_to(settings.root_resolved).as_posix()
    except ValueError:
        return path.as_posix()


@dataclass
class CodexSession:
    id: str
    title: str
    cwd: str
    model: str | None
    created_at: float
    updated_at: float
    codex_session_id: str | None = None
    status: str = "idle"
    exit_code: int | None = None
    prompts: list[dict] = field(default_factory=list)
    events: list[dict] = field(default_factory=list)
    queue: list[dict] = field(default_factory=list)
    clients: dict[WebSocket, WebSocketClient] = field(default_factory=dict)
    run_task: asyncio.Task | None = None
    process: asyncio.subprocess.Process | None = None
    meta_path: Path | None = None
    log_path: Path | None = None
    stderr_path: Path | None = None
    rollout_path: Path | None = None
    model_context_window: int | None = None
    context_used_percent: float | None = None
    total_tokens: int | None = None
    suppress_queue_drain: bool = False

    def summary(self) -> dict:
        return {
            "id": self.id,
            "codex_session_id": self.codex_session_id,
            "rollout_path": self.rollout_path.as_posix() if self.rollout_path else None,
            "title": self.title,
            "cwd": self.cwd,
            "cwd_relative": relative_cwd(self.cwd),
            "model": self.model,
            "created_at": self.created_at,
            "updated_at": self.updated_at,
            "status": self.status,
            "exit_code": self.exit_code,
            "event_count": len(self.events),
            "model_context_window": self.model_context_window,
            "context_used_percent": self.context_used_percent,
            "total_tokens": self.total_tokens,
            "queue": self.queue,
        }

    def snapshot(self) -> dict:
        return {**self.summary(), "prompts": self.prompts, "events": self.events}


class CodexSessionManager:
    def __init__(self) -> None:
        self.sessions: dict[str, CodexSession] = {}
        self._loaded = False
        self._status_cache: dict[str, Any] | None = None
        self._status_cache_key: tuple[int, int, int] | None = None

    def _paths(self, session_id: str) -> tuple[Path, Path, Path]:
        return (
            CODEX_LOG_DIR / f"{session_id}.json",
            CODEX_LOG_DIR / f"{session_id}.jsonl",
            CODEX_LOG_DIR / f"{session_id}.stderr.log",
        )

    def _cwd_for(self, cwd: str | None) -> str:
        return resolve_served_directory(cwd, "Codex")

    def _ensure_loaded(self) -> None:
        if self._loaded:
            return
        CODEX_LOG_DIR.mkdir(parents=True, exist_ok=True)
        for meta_path in sorted(CODEX_LOG_DIR.glob("*.json")):
            try:
                meta = json.loads(meta_path.read_text(encoding="utf-8"))
                session_id = str(meta["id"])
                _, log_path, stderr_path = self._paths(session_id)
                rollout_path = self._rollout_path_from_meta(meta.get("rollout_path"))
                status = meta.get("status", "exited")
                if status == "running":
                    status = "failed"
                session = CodexSession(
                    id=session_id,
                    codex_session_id=meta.get("codex_session_id"),
                    title=meta.get("title") or "Codex session",
                    cwd=meta.get("cwd") or settings.root_resolved.as_posix(),
                    model=meta.get("model"),
                    created_at=float(meta.get("created_at") or time.time()),
                    updated_at=float(meta.get("updated_at") or time.time()),
                    status=status,
                    exit_code=meta.get("exit_code"),
                    prompts=list(meta.get("prompts") or []),
                    queue=self._queue_from_meta(meta.get("queue")),
                    meta_path=meta_path,
                    log_path=log_path,
                    stderr_path=stderr_path,
                    rollout_path=rollout_path,
                )
                self._sync_rollout_events(session)
                self.sessions[session_id] = session
            except Exception:
                logger.warning("Failed to load Codex session metadata {}", meta_path)
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

    def _write_meta(self, session: CodexSession) -> None:
        if session.meta_path is None:
            return
        session.meta_path.parent.mkdir(parents=True, exist_ok=True)
        session.meta_path.write_text(
            json.dumps(
                {
                    "id": session.id,
                    "codex_session_id": session.codex_session_id,
                    "rollout_path": session.rollout_path.as_posix() if session.rollout_path else None,
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
            ),
            encoding="utf-8",
        )

    def _append_stderr(self, session: CodexSession, line: str) -> None:
        if session.stderr_path is None:
            return
        session.stderr_path.parent.mkdir(parents=True, exist_ok=True)
        with session.stderr_path.open("a", encoding="utf-8") as handle:
            handle.write(line)

    def _find_session_id(self, value: Any) -> str | None:
        if isinstance(value, dict):
            for key in ("session_id", "conversation_id", "thread_id"):
                found = value.get(key)
                if isinstance(found, str) and found:
                    return found
            for item in value.values():
                found = self._find_session_id(item)
                if found:
                    return found
        elif isinstance(value, list):
            for item in value:
                found = self._find_session_id(item)
                if found:
                    return found
        return None

    def _rollout_path_from_meta(self, value: Any) -> Path | None:
        if not isinstance(value, str) or not value:
            return None
        path = Path(value).expanduser()
        try:
            if path.exists() and path.is_file():
                return path
        except OSError:
            return None
        return None

    def _timestamp_value(self, raw: dict) -> float:
        timestamp = raw.get("timestamp")
        if isinstance(timestamp, str):
            try:
                parsed = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
                if parsed.tzinfo is None:
                    parsed = parsed.replace(tzinfo=timezone.utc)
                return parsed.timestamp()
            except ValueError:
                pass
        return time.time()

    def _rollout_session_id(self, path: Path) -> str | None:
        try:
            with path.open("r", encoding="utf-8", errors="replace") as handle:
                for line in handle:
                    if not line.strip():
                        continue
                    try:
                        raw = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    payload = raw.get("payload")
                    if raw.get("type") == "session_meta" and isinstance(payload, dict):
                        session_id = payload.get("id")
                        return session_id if isinstance(session_id, str) and session_id else None
        except OSError:
            return None
        return None

    def _find_rollout_for_session(self, codex_session_id: str | None) -> Path | None:
        if not codex_session_id or not CODEX_ROLLOUT_ROOT.exists():
            return None
        try:
            paths = sorted(CODEX_ROLLOUT_ROOT.rglob("rollout-*.jsonl"), key=lambda item: item.stat().st_mtime, reverse=True)
        except OSError:
            return None
        for path in paths:
            if self._rollout_session_id(path) == codex_session_id:
                return path
        return None

    def _raw_type(self, raw: dict) -> str:
        payload = raw.get("payload")
        if raw.get("type") in ("event_msg", "response_item") and isinstance(payload, dict):
            payload_type = payload.get("type")
            if isinstance(payload_type, str):
                return payload_type
        raw_type = raw.get("type")
        return raw_type if isinstance(raw_type, str) else "event"

    def _display_event_type(self, raw: dict) -> str:
        payload = raw.get("payload")
        if raw.get("type") in ("event_msg", "response_item") and isinstance(payload, dict):
            if payload.get("type") == "message" and isinstance(payload.get("role"), str):
                return f"message:{payload['role']}"
            payload_type = payload.get("type")
            if isinstance(payload_type, str):
                return payload_type
        if raw.get("type") == "custom_tool_call" and isinstance(raw.get("name"), str):
            return f"tool:{raw['name']}"
        item = raw.get("item")
        if isinstance(item, dict) and isinstance(item.get("type"), str):
            return item["type"]
        raw_type = raw.get("type")
        if isinstance(raw_type, str):
            return raw_type
        nested = raw.get("msg")
        if isinstance(nested, dict) and isinstance(nested.get("type"), str):
            return nested["type"]
        return "event"

    def _content_text(self, content: Any) -> str:
        if not isinstance(content, list):
            return ""
        parts: list[str] = []
        for item in content:
            if not isinstance(item, dict):
                continue
            text = item.get("text")
            output_text = item.get("output_text")
            if isinstance(text, str):
                parts.append(text)
            elif isinstance(output_text, str):
                parts.append(output_text)
        return "\n".join(parts)

    def _command_text(self, command: Any) -> str:
        if isinstance(command, list):
            return " ".join(str(item) for item in command)
        if isinstance(command, str):
            return command
        return ""

    def _text_from_value(self, value: Any, depth: int = 0) -> str:
        if depth > 6 or value is None:
            return ""
        if isinstance(value, str):
            return value
        if isinstance(value, (int, float, bool)):
            return str(value)
        if isinstance(value, list):
            return "\n".join(filter(None, (self._text_from_value(item, depth + 1) for item in value)))
        if not isinstance(value, dict):
            return ""

        if value.get("type") == "event_msg":
            payload = self._payload_of(value)
            if not payload:
                return ""
            payload_type = payload.get("type") if isinstance(payload.get("type"), str) else ""
            if payload_type == "exec_command_begin":
                command = self._command_text(payload.get("command"))
                return f"$ {command}" if command else ""
            if payload_type == "exec_command_end":
                command = self._command_text(payload.get("command"))
                output = payload.get("aggregated_output").strip() if isinstance(payload.get("aggregated_output"), str) else ""
                exit_code = f"exit {payload['exit_code']}" if isinstance(payload.get("exit_code"), int) else ""
                return "\n".join(filter(None, (f"$ {command}".strip(), output, exit_code)))
            if payload_type == "agent_message":
                message = payload.get("message")
                return message if isinstance(message, str) else ""
            if payload_type == "patch_apply_end":
                success = "Applied patch" if payload.get("success") is True else "Patch failed"
                stdout = payload.get("stdout").strip() if isinstance(payload.get("stdout"), str) else ""
                stderr = payload.get("stderr").strip() if isinstance(payload.get("stderr"), str) else ""
                return "\n".join(filter(None, (success, stdout, stderr)))
            if payload_type == "view_image_tool_call" and isinstance(payload.get("path"), str):
                return f"Viewed image: {payload['path']}"

        if value.get("type") == "response_item":
            payload = self._payload_of(value)
            if not payload:
                return ""
            if payload.get("type") == "message":
                role = payload.get("role") if isinstance(payload.get("role"), str) else ""
                if role != "assistant":
                    return ""
                return self._content_text(payload.get("content"))
            if payload.get("type") == "function_call" and isinstance(payload.get("name"), str):
                if payload.get("name") == "exec_command" and isinstance(payload.get("arguments"), str):
                    try:
                        args = json.loads(payload["arguments"])
                    except json.JSONDecodeError:
                        return f"Tool call: {payload['name']}"
                    if isinstance(args, dict) and isinstance(args.get("cmd"), str):
                        return f"$ {args['cmd']}"
                return f"Tool call: {payload['name']}"
            if payload.get("type") == "custom_tool_call" and isinstance(payload.get("name"), str):
                return "Applied patch" if payload.get("name") == "apply_patch" else f"Tool call: {payload['name']}"
            return ""

        if value.get("type") == "custom_tool_call" and isinstance(value.get("name"), str):
            return "Applied patch" if value.get("name") == "apply_patch" else f"Tool call: {value['name']}"

        for key in ("message", "text", "content", "output", "summary", "final_answer", "item"):
            found = self._text_from_value(value.get(key), depth + 1)
            if found:
                return found

        changes = value.get("changes")
        if isinstance(changes, list):
            rows: list[str] = []
            for change in changes:
                if not isinstance(change, dict):
                    continue
                kind = change.get("kind") if isinstance(change.get("kind"), str) else "change"
                path = change.get("path") if isinstance(change.get("path"), str) else ""
                rows.append(f"{kind}: {path}" if path else kind)
            return "\n".join(filter(None, rows))

        usage = value.get("usage")
        if isinstance(usage, dict):
            input_tokens = usage.get("input_tokens") if isinstance(usage.get("input_tokens"), int) else None
            output_tokens = usage.get("output_tokens") if isinstance(usage.get("output_tokens"), int) else None
            if input_tokens is not None or output_tokens is not None:
                return f"tokens: input {input_tokens if input_tokens is not None else '-'}, output {output_tokens if output_tokens is not None else '-'}"
        return ""

    def _normalize_message_text(self, text: str) -> str:
        return text.replace("\r\n", "\n").strip()

    def _is_assistant_response_item(self, raw: dict) -> bool:
        payload = self._payload_of(raw)
        return raw.get("type") == "response_item" and payload is not None and payload.get("type") == "message" and payload.get("role") == "assistant"

    def _is_duplicate_agent_message(self, raw: dict, text: str, assistant_response_texts: set[str]) -> bool:
        payload = self._payload_of(raw)
        return (
            raw.get("type") == "event_msg"
            and payload is not None
            and payload.get("type") == "agent_message"
            and self._normalize_message_text(text) in assistant_response_texts
        )

    def _extract_file_changes(self, raw: dict) -> list[dict]:
        direct = raw if raw.get("type") == "patch_apply_end" else None
        payload = self._payload_of(raw)
        wrapped = payload if raw.get("type") in ("event_msg", "response_item") and payload and payload.get("type") == "patch_apply_end" else None
        source = direct or wrapped
        if not source:
            return []
        changes = source.get("changes")
        if not isinstance(changes, dict):
            return []
        rows: list[dict] = []
        for path, value in changes.items():
            record = value if isinstance(value, dict) else {}
            rows.append(
                {
                    "path": str(path),
                    "change_type": record.get("type") if isinstance(record.get("type"), str) else "update",
                    "diff": record.get("unified_diff") if isinstance(record.get("unified_diff"), str) else None,
                }
            )
        return rows

    def _extract_patch_input(self, raw: dict) -> str | None:
        if raw.get("type") == "custom_tool_call" and raw.get("name") == "apply_patch" and isinstance(raw.get("input"), str):
            return raw["input"]
        payload = self._payload_of(raw)
        if (
            raw.get("type") == "response_item"
            and payload is not None
            and payload.get("type") == "custom_tool_call"
            and payload.get("name") == "apply_patch"
            and isinstance(payload.get("input"), str)
        ):
            return payload["input"]
        return None

    def _raw_preview(self, raw: dict) -> dict | None:
        encoded = json.dumps(raw, ensure_ascii=False, separators=(",", ":")).encode("utf-8", errors="replace")
        if len(encoded) <= RAW_PREVIEW_MAX_BYTES:
            return raw
        payload = self._payload_of(raw)
        preview: dict[str, Any] = {"type": raw.get("type"), "omitted_bytes": len(encoded)}
        if isinstance(raw.get("name"), str):
            preview["name"] = raw["name"]
        if payload:
            preview_payload: dict[str, Any] = {}
            for key in ("type", "role", "name"):
                if isinstance(payload.get(key), str):
                    preview_payload[key] = payload[key]
            preview["payload"] = preview_payload
        return preview

    def _compact_event(self, event: dict, assistant_response_texts: set[str]) -> dict | None:
        raw = event.get("raw")
        if not isinstance(raw, dict):
            return None
        event_type = self._display_event_type(raw)
        if event_type in HIDDEN_DISPLAY_EVENT_TYPES:
            return None
        text = self._text_from_value(raw)
        if self._is_duplicate_agent_message(raw, text, assistant_response_texts):
            return None
        file_changes = self._extract_file_changes(raw)
        patch_text = self._extract_patch_input(raw)
        if not text and not file_changes and not patch_text:
            return None
        return {
            "index": event["index"],
            "received_at": event["received_at"],
            "event_type": event_type,
            "text": text,
            "file_changes": file_changes,
            "patch_text": patch_text,
            "raw_preview": self._raw_preview(raw),
        }

    def _compact_events(self, events: list[dict]) -> list[dict]:
        assistant_response_texts = {
            self._normalize_message_text(self._text_from_value(event["raw"]))
            for event in events
            if isinstance(event.get("raw"), dict) and self._is_assistant_response_item(event["raw"])
        }
        assistant_response_texts.discard("")
        compacted: list[dict] = []
        for event in events:
            compact = self._compact_event(event, assistant_response_texts)
            if compact is not None:
                compacted.append(compact)
        return compacted

    def _snapshot(self, session: CodexSession) -> dict:
        return {**session.summary(), "prompts": session.prompts, "events": self._compact_events(session.events)}

    def snapshot(self, session_id: str) -> dict:
        session = self.get(session_id)
        return self._snapshot(session)

    def _turn_finished_status(self, events: list[dict]) -> str | None:
        status: str | None = None
        for event in events:
            raw = event.get("raw")
            if not isinstance(raw, dict):
                continue
            event_type = self._raw_type(raw)
            if event_type in ("task_complete", "turn.completed"):
                status = "exited"
            elif event_type in ("turn_aborted", "turn.failed"):
                status = "failed"
        return status

    def _payload_of(self, raw: dict) -> dict | None:
        payload = raw.get("payload")
        return payload if isinstance(payload, dict) else None

    def _usage_from_token_count(self, payload: dict) -> tuple[int | None, int | None, float | None]:
        info = payload.get("info")
        if not isinstance(info, dict):
            return None, None, None
        context_window = info.get("model_context_window")
        if not isinstance(context_window, int) or context_window <= 0:
            context_window = None
        usage = info.get("last_token_usage")
        if not isinstance(usage, dict):
            usage = info.get("total_token_usage")
        if not isinstance(usage, dict):
            return context_window, None, None
        total_tokens = usage.get("total_tokens")
        if not isinstance(total_tokens, int):
            return context_window, None, None
        used_percent = round((total_tokens / context_window) * 100, 1) if context_window else None
        return context_window, total_tokens, used_percent

    def _apply_session_rollout_status(self, session: CodexSession, events: list[dict]) -> None:
        for event in events:
            raw = event.get("raw")
            if not isinstance(raw, dict):
                continue
            payload = self._payload_of(raw)
            if not payload:
                continue
            event_type = raw.get("type")
            if event_type == "turn_context":
                model = payload.get("model")
                if isinstance(model, str) and model:
                    session.model = model
            if event_type != "event_msg" or payload.get("type") != "token_count":
                continue
            context_window, total_tokens, used_percent = self._usage_from_token_count(payload)
            if context_window is not None:
                session.model_context_window = context_window
            if total_tokens is not None:
                session.total_tokens = total_tokens
            if used_percent is not None:
                session.context_used_percent = used_percent

    def _sync_rollout_events(self, session: CodexSession) -> list[dict]:
        if session.rollout_path is None:
            session.rollout_path = self._find_rollout_for_session(session.codex_session_id)
            if session.rollout_path is not None:
                logger.info("Codex session {} matched rollout file {}", session.id, session.rollout_path)
                self._write_meta(session)
        if session.rollout_path is None or not session.rollout_path.exists():
            logger.debug("Codex session {} rollout file unavailable codex_session_id={}", session.id, session.codex_session_id)
            return []

        events: list[dict] = []
        try:
            with session.rollout_path.open("r", encoding="utf-8", errors="replace") as handle:
                for line in handle:
                    raw_line = line.rstrip("\n")
                    if not raw_line.strip():
                        continue
                    try:
                        raw = json.loads(raw_line)
                    except json.JSONDecodeError:
                        raw = {"type": "invalid_json", "line": raw_line}
                    events.append({"index": len(events), "received_at": self._timestamp_value(raw), "raw": raw})
        except OSError:
            return []

        old_count = len(session.events)
        session.events = events
        self._apply_session_rollout_status(session, events)
        new_events = events[old_count:]
        turn_status = self._turn_finished_status(new_events)
        if session.status == "running" and turn_status is not None:
            session.status = turn_status
            if turn_status == "exited" and session.exit_code is None:
                session.exit_code = 0
            logger.info("Codex session {} marked {} from rollout turn-finish event", session.id, turn_status)
        if events:
            session.updated_at = max(session.updated_at, float(events[-1]["received_at"]))
        if new_events:
            logger.debug(
                "Codex session {} synced {} new rollout event(s) total={} status={} clients={} path={}",
                session.id,
                len(new_events),
                len(events),
                session.status,
                len(session.clients),
                session.rollout_path,
            )
        self._write_meta(session)
        return new_events

    def _title_for(self, prompt: str) -> str:
        first = " ".join(prompt.strip().split())
        if not first:
            return "Codex session"
        return first[:72]

    def list(self) -> list[dict]:
        self._ensure_loaded()
        for session in self.sessions.values():
            if session.status == "running" and session.clients:
                continue
            self._sync_rollout_events(session)
        return sorted((session.summary() for session in self.sessions.values()), key=lambda item: item["updated_at"], reverse=True)

    def get(self, session_id: str) -> CodexSession:
        self._ensure_loaded()
        session = self.sessions.get(session_id)
        if not session:
            raise HTTPException(status_code=404, detail="Codex session not found")
        self._sync_rollout_events(session)
        return session

    async def create(self, prompt: str, cwd: str | None = None, model: str | None = None) -> dict:
        self._ensure_loaded()
        session_id = uuid.uuid4().hex
        meta_path, log_path, stderr_path = self._paths(session_id)
        now = time.time()
        cleaned_prompt = prompt.strip()
        session = CodexSession(
            id=session_id,
            title=self._title_for(cleaned_prompt) if cleaned_prompt else "New Codex session",
            cwd=self._cwd_for(cwd),
            model=model.strip() if isinstance(model, str) and model.strip() else None,
            created_at=now,
            updated_at=now,
            status="running" if cleaned_prompt else "idle",
            prompts=[{"text": cleaned_prompt, "created_at": now}] if cleaned_prompt else [],
            meta_path=meta_path,
            log_path=log_path,
            stderr_path=stderr_path,
        )
        self.sessions[session_id] = session
        stderr_path.write_text("", encoding="utf-8")
        self._write_meta(session)
        if cleaned_prompt:
            session.run_task = asyncio.create_task(self._run(session, cleaned_prompt, resume=False))
        return session.summary()

    async def enqueue(self, session_id: str, prompt: str, model: str | None = None) -> dict:
        session = self.get(session_id)
        cleaned_prompt = prompt.strip()
        if not cleaned_prompt:
            raise HTTPException(status_code=400, detail="Prompt is required")
        now = time.time()
        item = {
            "id": uuid.uuid4().hex,
            "prompt": cleaned_prompt,
            "created_at": now,
            "updated_at": now,
            "model": model.strip() if isinstance(model, str) and model.strip() else None,
        }
        session.queue.append(item)
        session.updated_at = now
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary()})
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
            now = time.time()
            item["prompt"] = cleaned_prompt
            item["updated_at"] = now
            item["model"] = model.strip() if isinstance(model, str) and model.strip() else item.get("model")
            session.updated_at = now
            self._write_meta(session)
            await self._broadcast(session, {"type": "status", "session": session.summary()})
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
        await self._broadcast(session, {"type": "status", "session": session.summary()})
        return session.summary()

    async def send(self, session_id: str, prompt: str, model: str | None = None) -> dict:
        session = self.get(session_id)
        if not prompt.strip():
            raise HTTPException(status_code=400, detail="Prompt is required")
        if session.status == "running":
            raise HTTPException(status_code=409, detail="Codex session is already running")
        resume = bool(session.codex_session_id)
        now = time.time()
        session.prompts.append({"text": prompt, "created_at": now})
        if model:
            session.model = model
        if not session.codex_session_id:
            session.title = self._title_for(prompt)
        session.status = "running"
        session.exit_code = None
        session.updated_at = now
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary()})
        session.run_task = asyncio.create_task(self._run(session, prompt, resume=resume))
        return session.summary()

    async def _start_next_queued(self, session: CodexSession) -> bool:
        if session.status == "running" or not session.queue:
            return False
        item = session.queue.pop(0)
        prompt = str(item.get("prompt") or "").strip()
        if not prompt:
            self._write_meta(session)
            await self._broadcast(session, {"type": "status", "session": session.summary()})
            return await self._start_next_queued(session)
        model = item.get("model")
        if isinstance(model, str) and model.strip():
            session.model = model.strip()
        now = time.time()
        session.prompts.append({"text": prompt, "created_at": now})
        if not session.codex_session_id:
            session.title = self._title_for(prompt)
        session.status = "running"
        session.exit_code = None
        session.updated_at = now
        session.suppress_queue_drain = False
        self._write_meta(session)
        await self._broadcast(session, {"type": "snapshot", "session": self._snapshot(session)})
        session.run_task = asyncio.create_task(self._run(session, prompt, resume=bool(session.codex_session_id)))
        return True

    async def delete(self, session_id: str) -> dict[str, str]:
        session = self.get(session_id)
        if session.process and session.process.returncode is None:
            session.process.terminate()
            try:
                await asyncio.wait_for(session.process.wait(), timeout=1.5)
            except asyncio.TimeoutError:
                session.process.kill()
                await session.process.wait()
        for path in (session.meta_path, session.log_path, session.stderr_path):
            if path:
                path.unlink(missing_ok=True)
        self.sessions.pop(session_id, None)
        await self._broadcast(session, {"type": "deleted"})
        return {"status": "deleted"}

    async def terminate(self, session_id: str) -> dict:
        session = self.get(session_id)
        if session.process and session.process.returncode is None:
            session.process.terminate()
            try:
                await asyncio.wait_for(session.process.wait(), timeout=1.5)
            except asyncio.TimeoutError:
                session.process.kill()
                await session.process.wait()
            session.exit_code = session.process.returncode
        session.process = None
        if session.status == "running":
            session.status = "exited"
        session.suppress_queue_drain = True
        session.updated_at = time.time()
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary()})
        return session.summary()

    async def connect(self, session_id: str, websocket: WebSocket) -> None:
        session = self.get(session_id)
        await websocket.accept()
        client = add_client(session.clients, websocket)
        enqueue(client, {"type": "snapshot", "session": self._snapshot(session)})
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

    async def shutdown(self) -> None:
        for session in list(self.sessions.values()):
            if session.process and session.process.returncode is None:
                session.process.terminate()

    async def resume_pending_queues(self) -> None:
        self._ensure_loaded()
        for session in list(self.sessions.values()):
            if session.status != "running" and session.queue:
                await self._start_next_queued(session)

    def cli_status(self) -> dict:
        rollouts = self._recent_rollouts()
        if not rollouts:
            return {"available": False}
        cache_key = self._rollout_cache_key(rollouts)
        if self._status_cache is not None and self._status_cache_key == cache_key:
            return self._status_cache
        status = self._latest_global_status(rollouts)
        self._status_cache = status
        self._status_cache_key = cache_key
        return status

    def _recent_rollouts(self, max_files: int = 40) -> list[Path]:
        if not CODEX_ROLLOUT_ROOT.exists():
            return []
        try:
            return sorted(CODEX_ROLLOUT_ROOT.rglob("rollout-*.jsonl"), key=lambda item: item.stat().st_mtime, reverse=True)[:max_files]
        except OSError:
            return []

    def _rollout_cache_key(self, paths: list[Path]) -> tuple[int, int, int]:
        newest_mtime = 0
        total_size = 0
        count = 0
        for path in paths:
            try:
                stat = path.stat()
            except OSError:
                continue
            count += 1
            newest_mtime = max(newest_mtime, int(stat.st_mtime))
            total_size += stat.st_size
        return count, newest_mtime, total_size

    def _tail_lines(self, path: Path, max_lines: int = 1800) -> list[str]:
        lines: deque[str] = deque(maxlen=max_lines)
        with path.open("r", encoding="utf-8", errors="replace") as handle:
            for line in handle:
                lines.append(line)
        return list(lines)

    def _latest_global_status(self, paths: list[Path]) -> dict:
        latest_status: dict | None = None
        latest_time = -1.0
        for path in paths:
            status = self._parse_rollout_status(path)
            updated_at = status.get("updated_at")
            if isinstance(updated_at, (int, float)) and updated_at > latest_time:
                latest_status = status
                latest_time = float(updated_at)
        return latest_status if latest_status is not None else {"available": False}

    def _rate_limit_percent(self, value: Any) -> float | None:
        if not isinstance(value, (int, float)):
            return None
        return round(max(0.0, min(100.0, float(value))), 1)

    def _parse_rollout_status(self, path: Path) -> dict:
        session_id: str | None = None
        updated_at: float | None = None
        cwd: str | None = None
        model: str | None = None
        context_window: int | None = None
        total_tokens: int | None = None
        context_used_percent: float | None = None
        plan_type: str | None = None
        primary_used_percent: float | None = None
        primary_remaining_percent: float | None = None
        primary_window_minutes: int | None = None
        secondary_used_percent: float | None = None
        secondary_remaining_percent: float | None = None
        secondary_window_minutes: int | None = None

        for line in self._tail_lines(path):
            raw = line.strip()
            if not raw:
                continue
            try:
                event = json.loads(raw)
            except json.JSONDecodeError:
                continue
            event_type = event.get("type")
            payload = event.get("payload")
            if not isinstance(payload, dict):
                continue
            if event_type == "session_meta":
                sid = payload.get("id")
                if isinstance(sid, str) and sid:
                    session_id = sid
                cwd_value = payload.get("cwd")
                if isinstance(cwd_value, str) and cwd_value:
                    cwd = cwd_value
            elif event_type == "turn_context":
                model_value = payload.get("model")
                if isinstance(model_value, str) and model_value:
                    model = model_value
                cwd_value = payload.get("cwd")
                if isinstance(cwd_value, str) and cwd_value:
                    cwd = cwd_value
            elif event_type == "event_msg":
                msg_type = payload.get("type")
                if msg_type != "token_count":
                    continue
                info = payload.get("info")
                if isinstance(info, dict):
                    context = info.get("model_context_window")
                    if isinstance(context, int) and context > 0:
                        context_window = context
                    usage = info.get("last_token_usage")
                    if not isinstance(usage, dict):
                        usage = info.get("total_token_usage")
                    if isinstance(usage, dict):
                        total = usage.get("total_tokens")
                        if isinstance(total, int):
                            total_tokens = total
                            if context_window and context_window > 0:
                                context_used_percent = round((total / context_window) * 100, 1)
                rate_limits = payload.get("rate_limits")
                if isinstance(rate_limits, dict):
                    plan = rate_limits.get("plan_type")
                    if isinstance(plan, str):
                        plan_type = plan
                    primary = rate_limits.get("primary")
                    if isinstance(primary, dict):
                        primary_used_percent = self._rate_limit_percent(primary.get("used_percent"))
                        if primary_used_percent is not None:
                            primary_remaining_percent = round(100.0 - primary_used_percent, 1)
                        window = primary.get("window_minutes")
                        if isinstance(window, int):
                            primary_window_minutes = window
                    secondary = rate_limits.get("secondary")
                    if isinstance(secondary, dict):
                        secondary_used_percent = self._rate_limit_percent(secondary.get("used_percent"))
                        if secondary_used_percent is not None:
                            secondary_remaining_percent = round(100.0 - secondary_used_percent, 1)
                        window = secondary.get("window_minutes")
                        if isinstance(window, int):
                            secondary_window_minutes = window
                updated_at = self._timestamp_value(event)

        return {
            "available": True,
            "session_id": session_id,
            "rollout_path": path.as_posix(),
            "updated_at": updated_at,
            "cwd": cwd,
            "model": model,
            "model_context_window": context_window,
            "context_used_percent": context_used_percent,
            "total_tokens": total_tokens,
            "plan_type": plan_type,
            "primary_used_percent": primary_used_percent,
            "primary_remaining_percent": primary_remaining_percent,
            "primary_window_minutes": primary_window_minutes,
            "secondary_used_percent": secondary_used_percent,
            "secondary_remaining_percent": secondary_remaining_percent,
            "secondary_window_minutes": secondary_window_minutes,
            "selected_model": model,
        }

    async def model_options(self) -> dict:
        from .files import read_config

        config = read_config().codex
        selected = config.default_model
        available = config.available_models or [selected]
        if selected not in available:
            available = [selected, *available]
        return {"selected_model": selected, "available_models": available, "source": "config"}

    def _codex_env(self) -> dict[str, str]:
        from .files import read_config

        env = dict(os.environ)
        proxy = read_config().codex.proxy.strip()
        if proxy:
            for key in PROXY_ENV_KEYS:
                env[key] = proxy
        else:
            for key in PROXY_ENV_KEYS:
                env.pop(key, None)
        return env

    async def _run(self, session: CodexSession, prompt: str, *, resume: bool) -> None:
        command = ["codex", "exec"]
        if resume:
            assert session.codex_session_id is not None
            command.extend([
                "resume",
                "--json",
                "--skip-git-repo-check",
                "--dangerously-bypass-approvals-and-sandbox",
                session.codex_session_id,
            ])
        else:
            command.extend([
                "--json",
                "--skip-git-repo-check",
                "--dangerously-bypass-approvals-and-sandbox",
                "-C",
                session.cwd,
            ])
        if session.model:
            command.extend(["-m", session.model])
        command.append("-")
        logger.info("Starting Codex session {} resume={} cwd={}", session.id, resume, session.cwd)
        try:
            process = await asyncio.create_subprocess_exec(
                *command,
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                cwd=session.cwd,
                env=self._codex_env(),
            )
        except FileNotFoundError:
            session.status = "failed"
            session.exit_code = 127
            session.updated_at = time.time()
            self._write_meta(session)
            await self._broadcast(session, {"type": "status", "session": session.summary()})
            return

        session.process = process
        assert process.stdin is not None
        process.stdin.write(prompt.encode())
        await process.stdin.drain()
        process.stdin.close()
        watch_stop = asyncio.Event()
        watch_task = asyncio.create_task(self._watch_rollout_events(session, process, watch_stop))
        try:
            await asyncio.gather(self._read_stdout(session, process), self._read_stderr(session, process))
        finally:
            watch_stop.set()
            watch_task.cancel()
            with suppress(asyncio.CancelledError):
                await watch_task
        exit_code = await process.wait()
        await self._sync_and_broadcast_rollout_events(session)
        session.exit_code = exit_code
        session.status = "exited" if exit_code == 0 else "failed"
        session.updated_at = time.time()
        session.process = None
        self._write_meta(session)
        logger.info("Codex session {} exited code={}", session.id, exit_code)
        await self._broadcast(session, {"type": "status", "session": session.summary()})
        if not session.suppress_queue_drain:
            await self._start_next_queued(session)
        session.suppress_queue_drain = False

    async def _read_stdout(self, session: CodexSession, process: asyncio.subprocess.Process) -> None:
        assert process.stdout is not None
        while True:
            line = await process.stdout.readline()
            if not line:
                return
            text = line.decode(encoding="utf-8", errors="replace")
            try:
                raw = json.loads(text)
            except json.JSONDecodeError:
                raw = {}
            codex_session_id = self._find_session_id(raw)
            if codex_session_id and codex_session_id != session.codex_session_id:
                session.codex_session_id = codex_session_id
                session.rollout_path = self._find_rollout_for_session(codex_session_id)
                logger.info(
                    "Codex session {} discovered codex_session_id={} rollout_path={}",
                    session.id,
                    codex_session_id,
                    session.rollout_path,
                )
                self._write_meta(session)
            await self._sync_and_broadcast_rollout_events(session)

    async def _watch_rollout_events(self, session: CodexSession, process: asyncio.subprocess.Process, stop_event: asyncio.Event) -> None:
        while process.returncode is None and not stop_event.is_set():
            if session.rollout_path is None:
                if session.codex_session_id:
                    session.rollout_path = self._find_rollout_for_session(session.codex_session_id)
                    if session.rollout_path is not None:
                        logger.info("Codex session {} rollout watcher matched {}", session.id, session.rollout_path)
                        self._write_meta(session)
                await self._sync_and_broadcast_rollout_events(session)
                await asyncio.sleep(0.25)
                continue

            path = session.rollout_path
            parent = path.parent
            logger.debug("Codex session {} watching rollout file {}", session.id, path)
            await self._sync_and_broadcast_rollout_events(session)
            if not parent.exists():
                logger.debug("Codex session {} rollout parent missing {}", session.id, parent)
                await asyncio.sleep(0.5)
                continue

            try:
                async for changes in awatch(
                    parent,
                    stop_event=stop_event,
                    watch_filter=lambda _change, raw_path: Path(raw_path) == path,
                    debounce=100,
                    debug=False,
                ):
                    if process.returncode is not None or stop_event.is_set() or session.rollout_path != path:
                        break
                    if any(Path(raw_path) == path for _change, raw_path in changes):
                        logger.debug("Codex session {} detected rollout file change {}", session.id, path)
                        await self._sync_and_broadcast_rollout_events(session)
            except OSError:
                logger.debug("Codex session {} rollout watcher failed for {}; retrying", session.id, path)
                await asyncio.sleep(0.5)

    async def _sync_and_broadcast_rollout_events(self, session: CodexSession) -> None:
        new_events = self._sync_rollout_events(session)
        assistant_response_texts = {
            self._normalize_message_text(self._text_from_value(event["raw"]))
            for event in session.events
            if isinstance(event.get("raw"), dict) and self._is_assistant_response_item(event["raw"])
        }
        assistant_response_texts.discard("")
        for event in new_events:
            raw = event.get("raw")
            event_type = self._raw_type(raw) if isinstance(raw, dict) else "event"
            logger.debug(
                "Codex session {} broadcasting rollout event index={} type={} status={} clients={}",
                session.id,
                event.get("index"),
                event_type,
                session.status,
                len(session.clients),
            )
            compact = self._compact_event(event, assistant_response_texts)
            if compact is not None:
                await self._broadcast(session, {"type": "event", "event": compact, "session": session.summary()})
            else:
                await self._broadcast(session, {"type": "status", "session": session.summary()})

    async def _read_stderr(self, session: CodexSession, process: asyncio.subprocess.Process) -> None:
        assert process.stderr is not None
        while True:
            line = await process.stderr.readline()
            if not line:
                return
            text = line.decode(encoding="utf-8", errors="replace")
            self._append_stderr(session, text)
            logger.debug("Codex session {} stderr: {}", session.id, text.rstrip())

    async def _broadcast(self, session: CodexSession, message: dict) -> None:
        stale = await broadcast(session.clients, message)
        logger.debug(
            "Codex session {} websocket broadcast type={} clients={} stale_removed={}",
            session.id,
            message.get("type"),
            len(session.clients),
            len(stale),
        )

    async def _remove_client(self, session: CodexSession, client: WebSocketClient) -> None:
        await remove_client(session.clients, client)


codex_session_manager = CodexSessionManager()
