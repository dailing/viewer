from __future__ import annotations

import asyncio
from contextlib import suppress
import json
import os
import time
import uuid
from pathlib import Path
from typing import Any

from fastapi import HTTPException
from loguru import logger

from .config import settings
from .files import resolve_served_directory
from .models import AgentEventType
from .identity import normalize_user_id
from .agent_history import agent_history_store
from .codex_app_server import CodexAppServerRuntime, CodexAppServerProcessConfig
from .storage import CODEX_APP_SERVER_LOG_DIR

# Well-known model context windows (tokens).
# Override with VIEWER_CODEX_APP_SERVER_CONTEXT_WINDOW or per-model via
# VIEWER_CODEX_APP_SERVER_CONTEXT_WINDOW_<MODEL_NAME> (e.g. _O3, _GPT_4O).
MODEL_CONTEXT_WINDOWS: dict[str, int] = {
    "o3": 200_000,
    "o4-mini": 200_000,
    "o4-mini-high": 200_000,
    "gpt-4o": 128_000,
    "gpt-4o-mini": 128_000,
    "gpt-4.1": 1_047_576,
    "gpt-4.1-mini": 1_047_576,
    "gpt-4.1-nano": 1_047_576,
    "gpt-4-turbo": 128_000,
    "gpt-4": 8_192,
    "gpt-3.5-turbo": 16_385,
}

DEFAULT_CONTEXT_WINDOW = 200_000


def _env_context_window(model: str | None) -> int:
    """Resolve context window for a model, with env var overrides."""
    if model:
        # Per-model env var: VIEWER_CODEX_APP_SERVER_CONTEXT_WINDOW_O3
        key = f"VIEWER_CODEX_APP_SERVER_CONTEXT_WINDOW_{model.upper().replace('-', '_').replace('.', '_')}"
        value = os.environ.get(key, "").strip()
        if value.isdigit():
            return int(value)
        # Lookup in well-known table
        if model in MODEL_CONTEXT_WINDOWS:
            return MODEL_CONTEXT_WINDOWS[model]
    # Global override
    global_value = os.environ.get("VIEWER_CODEX_APP_SERVER_CONTEXT_WINDOW", "").strip()
    if global_value.isdigit():
        return int(global_value)
    return DEFAULT_CONTEXT_WINDOW


RAW_PREVIEW_MAX_BYTES = 16 * 1024
AGENT_DETAIL_FOCUS = "focus"
AGENT_DETAIL_FULL = "full"
AGENT_DETAILS = {AGENT_DETAIL_FOCUS, AGENT_DETAIL_FULL}


def normalize_agent_detail(detail: str | None) -> str:
    return detail if detail in AGENT_DETAILS else AGENT_DETAIL_FOCUS


def relative_cwd(cwd: str) -> str:
    path = Path(cwd)
    try:
        return path.relative_to(settings.root_resolved).as_posix()
    except ValueError:
        return path.as_posix()


class CodexAppServerSession:
    def __init__(
        self,
        provider: str,
        id: str,
        user_id: str,
        title: str,
        cwd: str,
        model: str | None,
        created_at: float,
        updated_at: float,
        provider_session_id: str | None = None,
        status: str = "idle",
        exit_code: int | None = None,
        error: str | None = None,
        prompts: list[dict] | None = None,
        events: list[dict] | None = None,
        run_task: asyncio.Task | None = None,
        meta_path: Path | None = None,
        total_tokens: int | None = None,
        model_context_window: int | None = None,
        lineage: dict[str, str | None] | None = None,
    ) -> None:
        self.provider = provider
        self.id = id
        self.user_id = user_id
        self.title = title
        self.cwd = cwd
        self.model = model
        self.created_at = created_at
        self.updated_at = updated_at
        self.provider_session_id = provider_session_id
        self.status = status
        self.exit_code = exit_code
        self.error = error
        self.prompts = prompts or []
        self.events = events or []
        self.run_task = run_task
        self.meta_path = meta_path
        self.total_tokens = total_tokens
        self.model_context_window = model_context_window
        self.lineage = lineage or {}

    def summary(self) -> dict:
        return {
            "id": self.id,
            "provider": self.provider,
            "user_id": self.user_id,
            "provider_session_id": self.provider_session_id,
            "title": self.title,
            "cwd": self.cwd,
            "cwd_relative": relative_cwd(self.cwd),
            "model": self.model,
            "created_at": self.created_at,
            "updated_at": self.updated_at,
            "status": self.status,
            "exit_code": self.exit_code,
            "error": self.error,
            "event_count": len(self.events),
            "total_tokens": self.total_tokens,
            "model_context_window": self.model_context_window,
            "context_used_percent": (
                round(self.total_tokens * 100 / self.model_context_window, 2)
                if self.total_tokens is not None and self.model_context_window
                else None
            ),
            "transport": "codex-app-server",
        }


class CodexAppServerSessionManager:
    """Experimental Codex app-server session manager.

    Speaks Codex's native JSON-RPC protocol (thread/start, turn/start, etc.)
    instead of ACP. Events are normalized to Viewer AgentEvent IR.
    """

    def __init__(self) -> None:
        command = os.environ.get("VIEWER_CODEX_APP_SERVER_COMMAND", "codex").strip() or "codex"
        enabled = os.environ.get("VIEWER_CODEX_APP_SERVER_ENABLED", "true").strip().lower() not in {"0", "false", "no", "off"}
        self.runtime = CodexAppServerRuntime(
            CodexAppServerProcessConfig(
                provider="codex-app-server",
                command=command,
                arguments=("app-server", "--stdio"),
                enabled=enabled,
            ),
            self._handle_update,
        )
        self.metadata_dir = CODEX_APP_SERVER_LOG_DIR
        self.sessions: dict[str, CodexAppServerSession] = {}
        self._loaded = False
        self._notify_payloads: dict[str, tuple[str, dict[str, Any]]] = {}
        self._notify_tasks: dict[str, asyncio.Task] = {}

    def _paths(self, session_id: str) -> Path:
        return self.metadata_dir / f"{session_id}.json"

    def _cwd_for(self, cwd: str | None, user_id: str | None) -> str:
        return resolve_served_directory(cwd, self.provider, user_id)

    @property
    def provider(self) -> str:
        return "codex-app-server"

    def _ensure_loaded(self) -> None:
        if self._loaded:
            return
        self.metadata_dir.mkdir(parents=True, exist_ok=True)
        for meta_path in sorted(self.metadata_dir.glob("*.json")):
            try:
                meta = json.loads(meta_path.read_text(encoding="utf-8"))
                session_id = str(meta["id"])
                user_id = str(meta["user_id"])
                session = CodexAppServerSession(
                    provider=self.provider,
                    id=session_id,
                    user_id=user_id,
                    provider_session_id=meta.get("provider_session_id"),
                    title=meta.get("title") or f"{self.provider} session",
                    cwd=meta.get("cwd") or settings.root_resolved.as_posix(),
                    model=meta.get("model"),
                    created_at=float(meta.get("created_at") or time.time()),
                    updated_at=float(meta.get("updated_at") or time.time()),
                    status="exited" if meta.get("status") == "running" else meta.get("status", "exited"),
                    exit_code=meta.get("exit_code"),
                    error=meta.get("error") if isinstance(meta.get("error"), str) else None,
                    prompts=list(meta.get("prompts") or []),
                    meta_path=meta_path,
                    total_tokens=meta.get("total_tokens") if isinstance(meta.get("total_tokens"), int) else None,
                    model_context_window=(meta.get("model_context_window") if isinstance(meta.get("model_context_window"), int) else None),
                    lineage=meta.get("lineage") if isinstance(meta.get("lineage"), dict) else {},
                )
                self.sessions[session_id] = session
            except Exception:
                logger.warning("Failed to load {} session metadata {}", self.provider, meta_path)
        self._loaded = True

    def _write_meta(self, session: CodexAppServerSession) -> None:
        if session.meta_path is None:
            return
        session.meta_path.parent.mkdir(parents=True, exist_ok=True)
        payload = json.dumps(
            {
                "id": session.id,
                "provider": session.provider,
                "user_id": session.user_id,
                "provider_session_id": session.provider_session_id,
                "title": session.title,
                "cwd": session.cwd,
                "model": session.model,
                "created_at": session.created_at,
                "updated_at": session.updated_at,
                "status": session.status,
                "exit_code": session.exit_code,
                "error": session.error,
                "total_tokens": session.total_tokens,
                "model_context_window": session.model_context_window,
                "prompts": session.prompts,
                "lineage": session.lineage,
            },
            indent=2,
        )
        tmp_path = session.meta_path.with_suffix(f"{session.meta_path.suffix}.tmp")
        tmp_path.write_text(payload, encoding="utf-8")
        tmp_path.replace(session.meta_path)

    def _title_for(self, prompt: str) -> str:
        first = " ".join(prompt.strip().split())
        return first[:72] if first else f"{self.provider} session"

    def _prompt_text(self, prompt: str | list[dict[str, Any]]) -> str:
        if isinstance(prompt, str):
            return prompt.strip()
        parts: list[str] = []
        for block in prompt:
            if not isinstance(block, dict):
                continue
            if block.get("type") == "text" and isinstance(block.get("text"), str):
                parts.append(block["text"].strip())
            elif block.get("type") == "image" and isinstance(block.get("url"), str):
                parts.append(f"[Image: {block['url']}]")
            elif block.get("type") == "localImage" and isinstance(block.get("path"), str):
                parts.append(f"[Local image: {block['path']}]")
        return "\n".join(part for part in parts if part).strip()

    def _raw_preview(self, raw: dict) -> dict | None:
        encoded = json.dumps(raw, ensure_ascii=False, separators=(",", ":")).encode("utf-8", errors="replace")
        if len(encoded) <= RAW_PREVIEW_MAX_BYTES:
            return raw
        return {"type": raw.get("type"), "omitted_bytes": len(encoded)}

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

    def _record_lineage_events(self, session: CodexAppServerSession, events: list[dict], start_index: int) -> None:
        try:
            lineage = session.lineage
            for offset, event in enumerate(events):
                absolute_index = start_index + offset
                raw = event.get("raw_preview") if isinstance(event.get("raw_preview"), dict) else {"source": self.provider}
                raw = {**raw, "chat_id": lineage.get("chat_id"), "streaming": bool(event.get("streaming", False))}
                agent_history_store.record_provider_message(
                    user_id=session.user_id,
                    workspace_id=lineage.get("workspace_id"),
                    provider=self.provider,
                    viewer_session_id=session.id,
                    provider_session_id=session.provider_session_id,
                    query_message_id=lineage.get("query_message_id"),
                    driver_run_id=lineage.get("driver_run_id"),
                    parent_message_id=lineage.get("parent_message_id"),
                    sender_role_id=lineage.get("sender_role_id"),
                    recipient_role_id=lineage.get("recipient_role_id"),
                    role_id=lineage.get("role_id"),
                    event_index=absolute_index,
                    received_at=float(event.get("received_at") or time.time()),
                    source_path=None,
                    source_event_id=str(event.get("source_event_id") or f"{self.provider}:{session.provider_session_id}:{absolute_index}"),
                    source_line=None,
                    role="assistant",
                    event_type=str(event.get("event_type") or "operation"),
                    text=str(event.get("text") or ""),
                    raw=raw,
                    patch_text=event.get("patch_text") if isinstance(event.get("patch_text"), str) else None,
                    file_changes=event.get("file_changes") if isinstance(event.get("file_changes"), list) else None,
                )
            if events:
                self._notify_lineage(session, events[-1])
        except Exception:
            logger.exception("Failed to record lineage events for {}", self.provider)

    def _notify_lineage(self, session: CodexAppServerSession, event: dict[str, Any]) -> None:
        notify_url = os.environ.get("VIEWER_SUPER_WORKSPACE_NOTIFY_URL", "").strip()
        if not notify_url or not session.lineage:
            return
        payload = {
            "type": "run-updated",
            "user_id": session.user_id,
            "chat_id": session.lineage.get("chat_id"),
            "run_id": session.lineage.get("query_message_id"),
            "driver_run_id": session.lineage.get("driver_run_id"),
            "updated_at": time.time(),
            "provider_event": {
                "provider": self.provider,
                "viewer_session_id": session.id,
                "provider_session_id": session.provider_session_id,
                "index": event.get("index"),
                "event_type": str(event.get("event_type") or "operation"),
                "text": str(event.get("text") or ""),
                "patch_text": event.get("patch_text"),
                "file_changes": event.get("file_changes") or [],
                "streaming": bool(event.get("streaming", False)),
            },
        }
        self._notify_payloads[session.id] = (notify_url, payload)
        current = self._notify_tasks.get(session.id)
        if current is None or current.done():
            self._notify_tasks[session.id] = asyncio.create_task(self._flush_lineage_notifications(session.id))

    async def _flush_lineage_notifications(self, session_id: str) -> None:
        try:
            while session_id in self._notify_payloads:
                await asyncio.sleep(0.075)
                item = self._notify_payloads.pop(session_id, None)
                if item is None:
                    continue
                await asyncio.to_thread(self._post_lineage_notification, *item)
        finally:
            self._notify_tasks.pop(session_id, None)

    @staticmethod
    def _post_lineage_notification(notify_url: str, payload: dict[str, Any]) -> None:
        from urllib.request import Request, urlopen
        from urllib.error import URLError
        request = Request(
            notify_url,
            data=json.dumps(payload).encode("utf-8"),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urlopen(request, timeout=1.0) as response:
                response.read()
        except (OSError, URLError):
            logger.debug("Could not notify Viewer about Codex app-server stream update")

    def _event_for_detail(self, event: dict, detail: str) -> dict | None:
        if detail == AGENT_DETAIL_FULL:
            return event
        if event.get("event_type") != AgentEventType.MESSAGE_ASSISTANT:
            return None
        return {**event, "file_changes": [], "patch_text": None, "raw_preview": None}

    def _events_for_detail(self, events: list[dict], detail: str) -> list[dict]:
        normalized = normalize_agent_detail(detail)
        rows: list[dict] = []
        for event in events:
            next_event = self._event_for_detail(event, normalized)
            if next_event is not None:
                rows.append(next_event)
        return rows

    def _snapshot(self, session: CodexAppServerSession, detail: str | None = None) -> dict:
        return {
            **session.summary(),
            "prompts": session.prompts,
            "events": self._events_for_detail(session.events, normalize_agent_detail(detail)),
        }

    def snapshot(self, session_id: str, detail: str | None = None) -> dict:
        return self._snapshot(self.get(session_id), detail)

    def get(self, session_id: str) -> CodexAppServerSession:
        self._ensure_loaded()
        session = self.sessions.get(session_id)
        if not session:
            raise HTTPException(status_code=404, detail=f"{self.provider} session not found")
        return session

    async def create(
        self,
        prompt: str | list[dict[str, Any]],
        cwd: str | None = None,
        model: str | None = None,
        user_id: str | None = None,
        lineage: dict[str, str | None] | None = None,
    ) -> dict:
        self._ensure_loaded()
        session_id = uuid.uuid4().hex
        normalized_user_id = normalize_user_id(user_id)
        now = time.time()
        cleaned_prompt = self._prompt_text(prompt)
        session = CodexAppServerSession(
            provider=self.provider,
            id=session_id,
            user_id=normalized_user_id,
            provider_session_id=None,
            title=self._title_for(cleaned_prompt) if cleaned_prompt else f"New {self.provider} session",
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
            session.run_task = asyncio.create_task(self._run(session, prompt))
        return session.summary()

    async def send(
        self,
        session_id: str,
        prompt: str | list[dict[str, Any]],
        model: str | None = None,
        lineage: dict[str, str | None] | None = None,
    ) -> dict:
        session = self.get(session_id)
        cleaned_prompt = self._prompt_text(prompt)
        if not cleaned_prompt:
            raise HTTPException(status_code=400, detail="Prompt is required")
        if session.status == "running":
            raise HTTPException(status_code=409, detail=f"{self.provider} session is already running")
        if session.provider_session_id:
            await self.runtime.thread_resume(session.provider_session_id, session.cwd)
        now = time.time()
        session.prompts.append({"text": cleaned_prompt, "created_at": now})
        if model:
            session.model = model
        if lineage is not None:
            session.lineage = dict(lineage)
        if not session.events:
            session.title = self._title_for(cleaned_prompt)
        session.status = "running"
        session.exit_code = None
        session.error = None
        session.updated_at = now
        self._write_meta(session)
        session.run_task = asyncio.create_task(self._run(session, prompt, session_loaded=True))
        return session.summary()

    async def _run(self, session: CodexAppServerSession, prompt: str | list[dict[str, Any]], session_loaded: bool = False) -> None:
        try:
            if session.provider_session_id and not session_loaded:
                await self.runtime.thread_resume(session.provider_session_id, session.cwd)
            elif not session.provider_session_id:
                session.provider_session_id = await self.runtime.thread_start(session.cwd, session.model)
            self._write_meta(session)
            result = await self.runtime.turn_start(session.provider_session_id, prompt)
            turn = result.get("turn", {})
            turn_status = turn.get("status", "completed")
            failed = turn_status in {"failed", "interrupted"}
            session.status = "failed" if failed else "exited"
            session.exit_code = 1 if failed else 0
            turn_error = turn.get("error") if isinstance(turn.get("error"), dict) else {}
            session.error = str(turn_error.get("message") or f"Codex turn {turn_status}") if failed else None
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            session.status = "failed"
            session.exit_code = 1
            session.error = str(exc) or exc.__class__.__name__
            session.updated_at = time.time()
            self._write_meta(session)
            logger.exception("{} run failed session={}", self.provider, session.id)
        finally:
            session.updated_at = time.time()
            self._write_meta(session)
            session.run_task = None

    async def _handle_update(self, thread_id: str, update: dict[str, Any]) -> None:
        self._ensure_loaded()
        session = next((item for item in self.sessions.values() if item.provider_session_id == thread_id), None)
        if session is None:
            return
        method = update.get("method", "")
        params = update.get("params", {})
        raw = update.get("raw", {})
        event = self._normalized_event(session, method, params, raw)
        if event is not None:
            index = self._upsert_event(session, event)
            if session.lineage:
                self._record_lineage_events(session, [session.events[index]], index)
        if method == "turn/completed":
            self._finalize_streaming_events(session)
        session.updated_at = time.time()
        self._write_meta(session)

    def _upsert_event(self, session: CodexAppServerSession, event: dict[str, Any]) -> int:
        source_event_id = str(event.get("source_event_id") or "")
        if event.get("append") and source_event_id:
            for index in range(len(session.events) - 1, -1, -1):
                existing = session.events[index]
                if existing.get("source_event_id") != source_event_id:
                    continue
                event["text"] = f"{existing.get('text', '')}{event.get('text', '')}"
                event["index"] = index
                session.events[index] = event
                return index
        index = len(session.events)
        event["index"] = index
        session.events.append(event)
        return index

    def _finalize_streaming_events(self, session: CodexAppServerSession) -> None:
        for index, event in enumerate(session.events):
            if not event.get("streaming"):
                continue
            event["streaming"] = False
            if session.lineage:
                self._record_lineage_events(session, [event], index)

    def _normalized_event(self, session: CodexAppServerSession, method: str, params: dict[str, Any], raw: dict[str, Any]) -> dict[str, Any] | None:
        now = time.time()
        turn_id = str(params.get("turnId") or "unknown-turn")
        item_id = str(params.get("itemId") or "unknown-item")
        if method == "item/agentMessage/delta":
            delta = params.get("delta", "")
            if not delta:
                return None
            return {
                "source_event_id": f"{self.provider}:{session.provider_session_id}:{turn_id}:agent-message:{item_id}",
                "received_at": now,
                "event_type": AgentEventType.MESSAGE_ASSISTANT,
                "text": delta,
                "append": True,
                "streaming": True,
                "raw_preview": self._raw_preview(raw),
            }
        if method in {"item/reasoning/summaryTextDelta", "item/reasoning/textDelta"}:
            delta = params.get("delta", "")
            if not delta:
                return None
            return {
                "source_event_id": f"{self.provider}:{session.provider_session_id}:{turn_id}:reasoning:{item_id}",
                "received_at": now,
                "event_type": AgentEventType.REASONING,
                "text": delta,
                "append": True,
                "streaming": True,
                "raw_preview": self._raw_preview(raw),
            }
        if method == "turn/completed":
            turn = params.get("turn", {})
            status = turn.get("status", "completed")
            error = turn.get("error", {})
            text = error.get("message", "") if status == "failed" else ""
            return {
                "source_event_id": f"{self.provider}:{session.provider_session_id}:turn-completed:{turn.get('id') or turn_id}",
                "received_at": now,
                "event_type": AgentEventType.SESSION_UPDATE,
                "text": text,
                "append": False,
                "streaming": False,
                "raw_preview": self._raw_preview(raw),
            }
        if method == "item/commandExecution/outputDelta":
            output = params.get("delta", "")
            if not output:
                return None
            return {
                "source_event_id": f"{self.provider}:{session.provider_session_id}:{turn_id}:command:{item_id}",
                "received_at": now,
                "event_type": AgentEventType.TOOL_CALL,
                "text": output,
                "append": True,
                "streaming": True,
                "raw_preview": self._raw_preview(raw),
            }
        if method == "item/fileChange/patchUpdated":
            changes = params.get("changes") if isinstance(params.get("changes"), list) else []
            file_changes = []
            patches = []
            for change in changes:
                if not isinstance(change, dict):
                    continue
                path = str(change.get("path") or "")
                diff = str(change.get("diff") or "")
                kind = change.get("kind") if isinstance(change.get("kind"), dict) else {}
                change_type = str(kind.get("type") or "edit")
                if path:
                    file_changes.append({"path": path, "change_type": change_type, "diff": diff})
                if diff:
                    patches.append(diff)
            patch = "\n".join(patches)
            return {
                "source_event_id": f"{self.provider}:{session.provider_session_id}:{turn_id}:file-change:{item_id}",
                "received_at": now,
                "event_type": AgentEventType.PATCH_APPLY_END,
                "text": patch,
                "append": False,
                "streaming": False,
                "patch_text": patch,
                "file_changes": file_changes,
                "raw_preview": self._raw_preview(raw),
            }
        if method == "thread/tokenUsage/updated":
            usage = params.get("tokenUsage") if isinstance(params.get("tokenUsage"), dict) else {}
            # App Server's `total` is cumulative across every model call in the
            # thread/turn. It can exceed the model context window many times and
            # is not a measure of current context occupancy. `last` describes
            # the latest model call and is the value Codex exposes for context
            # window usage.
            last_usage = usage.get("last") if isinstance(usage.get("last"), dict) else {}
            context_tokens = last_usage.get("totalTokens")
            if isinstance(context_tokens, int):
                session.total_tokens = context_tokens
            context_window = usage.get("modelContextWindow")
            session.model_context_window = (
                context_window if isinstance(context_window, int) and context_window > 0 else _env_context_window(session.model)
            )
            return {
                "source_event_id": f"{self.provider}:{session.provider_session_id}:token-usage",
                "received_at": now,
                "event_type": AgentEventType.SESSION_UPDATE,
                "text": self._preview_text(usage),
                "append": False,
                "streaming": False,
                "raw_preview": self._raw_preview(raw),
            }
        # Unknown notification — log but don't create event
        logger.debug("Unknown {} notification: {} params={}", self.provider, method, params)
        return None

    async def start(self) -> None:
        self._ensure_loaded()
        await self.runtime.start()

    async def fork(self, session_id: str, cwd: str | None = None) -> dict[str, Any]:
        source = self.get(session_id)
        if not source.provider_session_id:
            raise HTTPException(status_code=409, detail=f"{self.provider} session has not started")
        resolved_cwd = self._cwd_for(cwd, source.user_id) if cwd else source.cwd
        provider_session_id = await self.runtime.thread_fork(source.provider_session_id, resolved_cwd)
        now = time.time()
        viewer_session_id = uuid.uuid4().hex
        forked = CodexAppServerSession(
            provider=self.provider,
            id=viewer_session_id,
            user_id=source.user_id,
            title=f"{source.title} (fork)",
            cwd=resolved_cwd,
            model=source.model,
            created_at=now,
            updated_at=now,
            provider_session_id=provider_session_id,
            status="exited",
            meta_path=self._paths(viewer_session_id),
            lineage=dict(source.lineage),
        )
        self.sessions[viewer_session_id] = forked
        self._write_meta(forked)
        return self._snapshot(forked, AGENT_DETAIL_FOCUS)

    async def resume(self, session_id: str) -> dict[str, Any]:
        session = self.get(session_id)
        if not session.provider_session_id:
            raise HTTPException(status_code=409, detail=f"{self.provider} session has not started")
        session.provider_session_id = await self.runtime.thread_resume(session.provider_session_id, session.cwd)
        session.status = "exited"
        session.error = None
        session.updated_at = time.time()
        self._write_meta(session)
        return self._snapshot(session, AGENT_DETAIL_FOCUS)

    async def terminate(self, session_id: str) -> dict:
        session = self.get(session_id)
        if session.provider_session_id:
            await self.runtime.turn_interrupt(session.provider_session_id)
        if session.run_task:
            with suppress(asyncio.TimeoutError):
                await asyncio.wait_for(asyncio.shield(session.run_task), timeout=5)
            if not session.run_task.done():
                session.run_task.cancel()
                with suppress(asyncio.CancelledError):
                    await session.run_task
            session.run_task = None
        if session.status == "running":
            session.status = "exited"
            session.exit_code = 130
            session.error = f"{self.provider} session was cancelled"
        session.updated_at = time.time()
        self._write_meta(session)
        return session.summary()

    async def shutdown(self) -> None:
        for task in list(self._notify_tasks.values()):
            task.cancel()
        if self._notify_tasks:
            await asyncio.gather(*self._notify_tasks.values(), return_exceptions=True)
        self._notify_tasks.clear()
        self._notify_payloads.clear()
        for session in list(self.sessions.values()):
            if session.run_task:
                session.run_task.cancel()
        for session in list(self.sessions.values()):
            if session.run_task:
                with suppress(asyncio.CancelledError):
                    await session.run_task


codex_app_server_session_manager = CodexAppServerSessionManager()
