import asyncio
from collections import deque
from contextlib import suppress
from datetime import datetime, timezone
import json
import os
import signal
import subprocess
import sys
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
from .models import AgentEventType
from .process_registry import driver_process_name, process_slot_state
from .storage import AGENT_HISTORY_DB_PATH, CODEX_LOG_DIR, CODEX_RUN_DIR
from .users import default_user_id, normalize_user_id
from .ws_clients import WebSocketClient, add_client, enqueue, remove_client

CODEX_ROLLOUT_ROOT = Path.home() / ".codex" / "sessions"
PROXY_ENV_KEYS = ("https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY")
RAW_PREVIEW_MAX_BYTES = 16 * 1024
AGENT_DETAIL_FOCUS = "focus"
AGENT_DETAIL_FULL = "full"
AGENT_DETAILS = {AGENT_DETAIL_FOCUS, AGENT_DETAIL_FULL}
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
CODEX_AGENT_EVENT_TYPE_MAP = {
    "agent_message": AgentEventType.MESSAGE_ASSISTANT,
    "custom_tool_call": AgentEventType.CUSTOM_TOOL_CALL,
    "exec_command_begin": AgentEventType.EXEC_COMMAND_BEGIN,
    "exec_command_end": AgentEventType.EXEC_COMMAND_END,
    "function_call": AgentEventType.FUNCTION_CALL,
    "message:assistant": AgentEventType.MESSAGE_ASSISTANT,
    "patch_apply_end": AgentEventType.PATCH_APPLY_END,
    "view_image_tool_call": AgentEventType.VIEW_IMAGE_TOOL_CALL,
}


def normalize_agent_detail(detail: str | None) -> str:
    return detail if detail in AGENT_DETAILS else AGENT_DETAIL_FOCUS


def relative_cwd(cwd: str) -> str:
    path = Path(cwd)
    try:
        return path.relative_to(settings.root_resolved).as_posix()
    except ValueError:
        return path.as_posix()


@dataclass
class CodexSession:
    id: str
    user_id: str
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
    process: subprocess.Popen | None = None
    pid: int | None = None
    codex_pid: int | None = None
    run_id: str | None = None
    run_started_at: float | None = None
    run_state_path: Path | None = None
    run_stdout_path: Path | None = None
    run_stderr_path: Path | None = None
    run_prompt_path: Path | None = None
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
            "user_id": self.user_id,
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
            "pid": self.pid,
            "codex_pid": self.codex_pid,
            "run_id": self.run_id,
            "run_started_at": self.run_started_at,
            "event_count": len(self.events),
            "model_context_window": self.model_context_window,
            "context_used_percent": self.context_used_percent,
            "total_tokens": self.total_tokens,
            "queue": self.queue,
            "pending_approvals": [],
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

    def _cwd_for(self, cwd: str | None, user_id: str | None) -> str:
        return resolve_served_directory(cwd, "Codex", user_id)

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
                session = CodexSession(
                    id=session_id,
                    user_id=meta.get("user_id") or default_user_id(),
                    codex_session_id=meta.get("codex_session_id"),
                    title=meta.get("title") or "Codex session",
                    cwd=meta.get("cwd") or settings.root_resolved.as_posix(),
                    model=meta.get("model"),
                    created_at=float(meta.get("created_at") or time.time()),
                    updated_at=float(meta.get("updated_at") or time.time()),
                    status=status,
                    exit_code=meta.get("exit_code"),
                    pid=meta.get("pid") if isinstance(meta.get("pid"), int) else None,
                    codex_pid=meta.get("codex_pid") if isinstance(meta.get("codex_pid"), int) else None,
                    run_id=meta.get("run_id") if isinstance(meta.get("run_id"), str) else None,
                    run_started_at=float(meta["run_started_at"]) if isinstance(meta.get("run_started_at"), (int, float)) else None,
                    run_state_path=self._path_from_meta(meta.get("run_state_path")),
                    run_stdout_path=self._path_from_meta(meta.get("run_stdout_path")),
                    run_stderr_path=self._path_from_meta(meta.get("run_stderr_path")),
                    run_prompt_path=self._path_from_meta(meta.get("run_prompt_path")),
                    prompts=list(meta.get("prompts") or []),
                    queue=self._queue_from_meta(meta.get("queue")),
                    meta_path=meta_path,
                    log_path=log_path,
                    stderr_path=stderr_path,
                    rollout_path=rollout_path,
                )
                self._sync_background_run_state(session)
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
        payload = json.dumps(
            {
                "id": session.id,
                "user_id": session.user_id,
                "codex_session_id": session.codex_session_id,
                "rollout_path": session.rollout_path.as_posix() if session.rollout_path else None,
                "title": session.title,
                "cwd": session.cwd,
                "model": session.model,
                "created_at": session.created_at,
                "updated_at": session.updated_at,
                "status": session.status,
                "exit_code": session.exit_code,
                "pid": session.pid,
                "codex_pid": session.codex_pid,
                "run_id": session.run_id,
                "run_started_at": session.run_started_at,
                "run_state_path": session.run_state_path.as_posix() if session.run_state_path else None,
                "run_stdout_path": session.run_stdout_path.as_posix() if session.run_stdout_path else None,
                "run_stderr_path": session.run_stderr_path.as_posix() if session.run_stderr_path else None,
                "run_prompt_path": session.run_prompt_path.as_posix() if session.run_prompt_path else None,
                "prompts": session.prompts,
                "queue": session.queue,
            },
            indent=2,
        )
        tmp_path = session.meta_path.with_suffix(f"{session.meta_path.suffix}.tmp")
        tmp_path.write_text(payload, encoding="utf-8")
        tmp_path.replace(session.meta_path)

    def _append_stderr(self, session: CodexSession, line: str) -> None:
        if session.stderr_path is None:
            return
        session.stderr_path.parent.mkdir(parents=True, exist_ok=True)
        with session.stderr_path.open("a", encoding="utf-8") as handle:
            handle.write(line)

    def _run_paths(self, session_id: str, run_id: str) -> tuple[Path, Path, Path, Path]:
        run_dir = CODEX_RUN_DIR / session_id / run_id
        return (
            run_dir / "state.json",
            run_dir / "stdout.jsonl",
            run_dir / "stderr.log",
            run_dir / "prompt.txt",
        )

    def _read_run_state(self, session: CodexSession) -> dict:
        if session.run_state_path is None or not session.run_state_path.exists():
            return {}
        try:
            value = json.loads(session.run_state_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            return {}
        return value if isinstance(value, dict) else {}

    def _pid_alive(self, pid: int | None) -> bool:
        if not isinstance(pid, int) or pid <= 0:
            return False
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return False
        except PermissionError:
            return True
        return True

    def _sync_stdout_session_id(self, session: CodexSession) -> None:
        if session.run_stdout_path is None or not session.run_stdout_path.exists():
            return
        try:
            with session.run_stdout_path.open("r", encoding="utf-8", errors="replace") as handle:
                for line in handle:
                    if not line.strip():
                        continue
                    try:
                        raw = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    codex_session_id = self._find_session_id(raw)
                    if codex_session_id and codex_session_id != session.codex_session_id:
                        session.codex_session_id = codex_session_id
                        session.rollout_path = self._find_rollout_for_session(codex_session_id)
                        logger.info(
                            "Codex session {} discovered codex_session_id={} rollout_path={} from background stdout",
                            session.id,
                            codex_session_id,
                            session.rollout_path,
                        )
                        self._write_meta(session)
        except OSError:
            return

    def _sync_background_run_state(self, session: CodexSession) -> None:
        self._sync_stdout_session_id(session)
        state = self._read_run_state(session)
        runner_pid = state.get("runner_pid")
        codex_pid = state.get("codex_pid")
        if isinstance(runner_pid, int):
            session.pid = runner_pid
        if isinstance(codex_pid, int):
            session.codex_pid = codex_pid
        state_status = state.get("status")
        exit_code = state.get("exit_code")
        codex_session_id = state.get("codex_session_id")
        rollout_path = self._rollout_path_from_meta(state.get("rollout_path"))
        if isinstance(codex_session_id, str) and codex_session_id:
            session.codex_session_id = codex_session_id
        if rollout_path is not None:
            session.rollout_path = rollout_path
        if isinstance(exit_code, int):
            session.exit_code = exit_code

        if state_status in ("exited", "failed"):
            session.status = "exited" if state_status == "exited" else "failed"
            return

        if state_status in ("starting", "running") and self._pid_alive(session.pid):
            session.status = "running"
            session.exit_code = None
            return

        if session.status == "running" and not self._pid_alive(session.pid):
            started_at = session.run_started_at or session.updated_at
            if started_at and time.time() - started_at < 10.0:
                return
            session.status = "failed"
            if session.exit_code is None:
                session.exit_code = -1

    def _background_run_active(self, session: CodexSession) -> bool:
        if session.process is not None and session.process.poll() is None:
            return True
        state = self._read_run_state(session)
        if state.get("status") in ("exited", "failed"):
            return False
        return self._pid_alive(session.pid)

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

    def _path_from_meta(self, value: Any) -> Path | None:
        if not isinstance(value, str) or not value:
            return None
        return Path(value).expanduser()

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
            return "custom_tool_call"
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

    def _agent_event_type(self, display_event_type: str, raw: dict) -> AgentEventType:
        event_type = CODEX_AGENT_EVENT_TYPE_MAP.get(display_event_type)
        if event_type:
            return event_type
        logger.warning(
            "Unmapped Codex display event type event_type={} raw_preview={}",
            display_event_type,
            self._raw_preview(raw),
        )
        return AgentEventType.OPERATION

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
        display_event_type = self._display_event_type(raw)
        if display_event_type in HIDDEN_DISPLAY_EVENT_TYPES:
            return None
        text = self._text_from_value(raw)
        if self._is_duplicate_agent_message(raw, text, assistant_response_texts):
            return None
        file_changes = self._extract_file_changes(raw)
        patch_text = self._extract_patch_input(raw)
        if not text and not file_changes and not patch_text:
            return None
        event_type = self._agent_event_type(display_event_type, raw)
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

    def _snapshot(self, session: CodexSession, detail: str | None = None) -> dict:
        compact_events = self._compact_events(session.events)
        return {**session.summary(), "prompts": session.prompts, "events": self._events_for_detail(compact_events, normalize_agent_detail(detail))}

    def snapshot(self, session_id: str, detail: str | None = None) -> dict:
        session = self.get(session_id)
        return self._snapshot(session, detail)

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
        runner_active = self._background_run_active(session)
        if session.status == "running" and turn_status is not None and not runner_active:
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

    def list(self, user_id: str | None = None) -> list[dict]:
        self._ensure_loaded()
        normalized_user_id = normalize_user_id(user_id)
        for session in self.sessions.values():
            self._sync_background_run_state(session)
        return sorted(
            (session.summary() for session in self.sessions.values() if session.user_id == normalized_user_id),
            key=lambda item: item["updated_at"],
            reverse=True,
        )

    def get(self, session_id: str) -> CodexSession:
        self._ensure_loaded()
        session = self.sessions.get(session_id)
        if not session:
            raise HTTPException(status_code=404, detail="Codex session not found")
        self._sync_background_run_state(session)
        self._sync_rollout_events(session)
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
        meta_path, log_path, stderr_path = self._paths(session_id)
        now = time.time()
        cleaned_prompt = prompt.strip()
        session = CodexSession(
            id=session_id,
            user_id=normalized_user_id,
            title=self._title_for(cleaned_prompt) if cleaned_prompt else "New Codex session",
            cwd=self._cwd_for(cwd, normalized_user_id),
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
            session.run_task = asyncio.create_task(self._run(session, cleaned_prompt, resume=False, lineage=lineage))
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
        if not self._background_run_active(session):
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

    async def send(self, session_id: str, prompt: str, model: str | None = None, lineage: dict[str, str | None] | None = None) -> dict:
        session = self.get(session_id)
        if not prompt.strip():
            raise HTTPException(status_code=400, detail="Prompt is required")
        if session.status == "running" or self._background_run_active(session):
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
        session.run_task = asyncio.create_task(self._run(session, prompt, resume=resume, lineage=lineage))
        return session.summary()

    async def _start_next_queued(self, session: CodexSession) -> bool:
        if session.status == "running" or self._background_run_active(session) or not session.queue:
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
        await self._broadcast(session, {"type": "snapshot", "session": self._snapshot(session, AGENT_DETAIL_FULL)})
        session.run_task = asyncio.create_task(self._run(session, prompt, resume=bool(session.codex_session_id)))
        return True

    async def terminate(self, session_id: str) -> dict:
        session = self.get(session_id)
        self._sync_background_run_state(session)
        if session.pid and self._pid_alive(session.pid):
            with suppress(OSError):
                os.killpg(session.pid, signal.SIGTERM)
            await asyncio.sleep(1.5)
            if self._pid_alive(session.pid):
                with suppress(OSError):
                    os.killpg(session.pid, signal.SIGKILL)
        if session.process is not None:
            with suppress(Exception):
                session.process.wait(timeout=0)
            session.exit_code = session.process.returncode
        session.process = None
        if session.run_task:
            session.run_task.cancel()
            with suppress(asyncio.CancelledError):
                await session.run_task
            session.run_task = None
        if session.status == "running":
            session.status = "exited"
        session.suppress_queue_drain = True
        session.updated_at = time.time()
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary()})
        return session.summary()

    async def resolve_approval(self, session_id: str, approval_id: str, choice: str, resolve_all: bool = False) -> dict:
        self.get(session_id)
        raise HTTPException(status_code=409, detail="Codex session has no pending approvals")

    async def connect(self, session_id: str, websocket: WebSocket, detail: str | None = None) -> None:
        session = self.get(session_id)
        normalized_detail = normalize_agent_detail(detail)
        await websocket.accept()
        client = add_client(session.clients, websocket)
        setattr(client, "agent_detail", normalized_detail)
        enqueue(client, {"type": "snapshot", "session": self._snapshot(session, normalized_detail), "source": "codex"})
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
            if session.run_task:
                session.run_task.cancel()
        for session in list(self.sessions.values()):
            if session.run_task:
                with suppress(asyncio.CancelledError):
                    await session.run_task

    async def resume_pending_queues(self) -> None:
        self._ensure_loaded()
        for session in list(self.sessions.values()):
            self._sync_background_run_state(session)
            if self._background_run_active(session) and session.run_task is None:
                session.run_task = asyncio.create_task(self._monitor_background_run(session))
                continue
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

    async def _run(self, session: CodexSession, prompt: str, *, resume: bool, lineage: dict[str, str | None] | None = None) -> None:
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
        run_id = uuid.uuid4().hex
        state_path, stdout_path, stderr_path, prompt_path = self._run_paths(session.id, run_id)
        state_path.parent.mkdir(parents=True, exist_ok=True)
        prompt_path.write_text(prompt, encoding="utf-8")
        stdout_path.write_text("", encoding="utf-8")
        stderr_path.write_text("", encoding="utf-8")
        session.run_id = run_id
        session.run_started_at = time.time()
        session.run_state_path = state_path
        session.run_stdout_path = stdout_path
        session.run_stderr_path = stderr_path
        session.run_prompt_path = prompt_path
        session.pid = None
        session.codex_pid = None
        self._write_meta(session)
        runner = Path(__file__).with_name("codex_background_runner.py")
        runner_command = [
            sys.executable,
            runner.as_posix(),
            "--state",
            state_path.as_posix(),
            "--stdout",
            stdout_path.as_posix(),
            "--stderr",
            stderr_path.as_posix(),
            "--prompt",
            prompt_path.as_posix(),
            "--cwd",
            session.cwd,
            "--command",
            json.dumps(command),
            "--history-db",
            AGENT_HISTORY_DB_PATH.as_posix(),
            "--viewer-session-id",
            session.id,
            "--user-id",
            session.user_id,
        ]
        for key, option in (
            ("workspace_id", "--workspace-id"),
            ("chat_id", "--chat-id"),
            ("query_message_id", "--query-message-id"),
            ("driver_run_id", "--driver-run-id"),
            ("parent_message_id", "--parent-message-id"),
            ("sender_role_id", "--sender-role-id"),
            ("recipient_role_id", "--recipient-role-id"),
            ("role_id", "--role-id"),
            ("role_name", "--role-name"),
        ):
            value = lineage.get(key) if lineage else None
            if isinstance(value, str) and value:
                runner_command.extend([option, value])
        registry_name = self._driver_registry_name(lineage)
        if registry_name:
            slot = process_slot_state(registry_name)
            if slot["pid_file_exists"] and slot["alive"]:
                logger.error(
                    "Codex driver pid file already points to a live process; not starting duplicate session={} pid={} pid_file={}",
                    session.id,
                    slot["pid"],
                    slot["pid_path"],
                )
                session.status = "failed"
                session.exit_code = -2
                session.updated_at = time.time()
                self._write_meta(session)
                await self._broadcast(session, {"type": "status", "session": session.summary()})
                return
            if slot["pid_file_exists"]:
                logger.warning(
                    "Stale Codex driver pid file found; overwriting session={} pid={} pid_file={}",
                    session.id,
                    slot["pid"],
                    slot["pid_path"],
                )
        logger.info("Starting background Codex session {} resume={} cwd={} run_dir={}", session.id, resume, session.cwd, state_path.parent)
        try:
            process = subprocess.Popen(
                runner_command,
                stdin=subprocess.DEVNULL,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                cwd=session.cwd,
                env=self._codex_env(),
                start_new_session=True,
            )
        except FileNotFoundError:
            session.status = "failed"
            session.exit_code = 127
            session.updated_at = time.time()
            self._write_meta(session)
            await self._broadcast(session, {"type": "status", "session": session.summary()})
            return

        session.process = process
        session.pid = process.pid
        self._write_meta(session)
        await self._monitor_background_run(session)

    def _driver_registry_name(self, lineage: dict[str, str | None] | None) -> str | None:
        if not lineage:
            return None
        driver_run_id = lineage.get("driver_run_id")
        role_id = lineage.get("role_id")
        if not driver_run_id or not role_id:
            return None
        return driver_process_name(lineage.get("role_name"), role_id, driver_run_id)

    async def _monitor_background_run(self, session: CodexSession) -> None:
        try:
            while True:
                previous_status = session.status
                self._sync_background_run_state(session)
                await self._sync_and_broadcast_rollout_events(session)
                if session.process is not None:
                    exit_code = session.process.poll()
                    if exit_code is not None:
                        session.exit_code = exit_code
                        if session.status == "running":
                            session.status = "exited" if exit_code == 0 else "failed"
                elif session.status == "running" and not self._pid_alive(session.pid):
                    session.status = "failed"
                    if session.exit_code is None:
                        session.exit_code = -1

                if session.status != previous_status:
                    session.updated_at = time.time()
                    self._write_meta(session)
                    await self._broadcast(session, {"type": "status", "session": session.summary()})

                runner_alive = self._background_run_active(session)
                if session.status != "running" and runner_alive:
                    await asyncio.sleep(0.5)
                    continue

                if session.status != "running":
                    if session.process is not None:
                        with suppress(Exception):
                            session.process.wait(timeout=0)
                    session.process = None
                    self._sync_background_run_state(session)
                    await self._sync_and_broadcast_rollout_events(session)
                    session.updated_at = time.time()
                    self._write_meta(session)
                    logger.info("Background Codex session {} finished status={} code={}", session.id, session.status, session.exit_code)
                    await self._broadcast(session, {"type": "status", "session": session.summary()})
                    if not session.suppress_queue_drain:
                        await self._start_next_queued(session)
                    session.suppress_queue_drain = False
                    return

                await asyncio.sleep(0.5)
        except asyncio.CancelledError:
            logger.info("Background Codex monitor detached session={} pid={}", session.id, session.pid)
            raise

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
        message.setdefault("source", "codex")
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
        logger.debug(
            "Codex session {} websocket broadcast type={} clients={} stale_removed={}",
            session.id,
            message.get("type"),
            len(session.clients),
            len(stale),
        )

    def _message_for_detail(self, session: CodexSession, message: dict, detail: str) -> dict | None:
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

    async def _remove_client(self, session: CodexSession, client: WebSocketClient) -> None:
        await remove_client(session.clients, client)


codex_session_manager = CodexSessionManager()
