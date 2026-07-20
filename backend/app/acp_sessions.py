import asyncio
from contextlib import suppress
import json
import os
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any
from urllib.error import URLError
from urllib.request import Request, urlopen

from fastapi import HTTPException
from loguru import logger

from .config import settings
from .files import resolve_served_directory
from .models import AgentEventType
from .identity import normalize_user_id
from .acp_runtime import ACPRuntime

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


@dataclass
class ACPSession:
    provider: str
    id: str
    user_id: str
    title: str
    cwd: str
    model: str | None
    created_at: float
    updated_at: float
    provider_session_id: str | None = None
    provider_profile: str = "default"
    status: str = "idle"
    exit_code: int | None = None
    error: str | None = None
    prompts: list[dict] = field(default_factory=list)
    events: list[dict] = field(default_factory=list)
    run_task: asyncio.Task | None = None
    meta_path: Path | None = None
    total_tokens: int | None = None
    model_context_window: int | None = None
    lineage: dict[str, str | None] = field(default_factory=dict)
    acp_events: list[dict] = field(default_factory=list)
    acp_event_keys: dict[str, int] = field(default_factory=dict)
    acp_turn_index: int = 0
    loading_provider_history: bool = False
    available_commands: list[dict] = field(default_factory=list)
    current_mode: str | None = None
    config_options: list[dict] = field(default_factory=list)
    yolo: bool = False

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
            "transport": "acp",
            "provider_profile": self.provider_profile,
            "yolo": self.yolo,
            "available_commands": self.available_commands,
            "current_mode": self.current_mode,
            "config_options": self.config_options,
        }


class ACPSessionManager:
    def __init__(
        self,
        provider: str,
        acp: ACPRuntime,
        metadata_dir: Path,
        *,
        legacy_provider_session_key: str | None = None,
        legacy_profile_key: str | None = None,
    ) -> None:
        self.provider = provider
        self.acp = acp
        self.metadata_dir = metadata_dir
        self.legacy_provider_session_key = legacy_provider_session_key
        self.legacy_profile_key = legacy_profile_key
        self.sessions: dict[str, ACPSession] = {}
        self._loaded = False
        self._notify_payloads: dict[str, tuple[str, dict[str, Any]]] = {}
        self._notify_tasks: dict[str, asyncio.Task] = {}

    def _paths(self, session_id: str) -> Path:
        return self.metadata_dir / f"{session_id}.json"

    def _cwd_for(self, cwd: str | None, user_id: str | None) -> str:
        return resolve_served_directory(cwd, self.provider, user_id)

    def _ensure_loaded(self) -> None:
        if self._loaded:
            return
        self.metadata_dir.mkdir(parents=True, exist_ok=True)
        for meta_path in sorted(self.metadata_dir.glob("*.json")):
            try:
                meta = json.loads(meta_path.read_text(encoding="utf-8"))
                session_id = str(meta["id"])
                user_id = str(meta["user_id"])
                session = ACPSession(
                    provider=self.provider,
                    id=session_id,
                    user_id=user_id,
                    provider_session_id=(
                        meta.get("provider_session_id")
                        or (meta.get(self.legacy_provider_session_key) if self.legacy_provider_session_key else None)
                    ),
                    provider_profile=str(
                        meta.get("provider_profile")
                        or (meta.get(self.legacy_profile_key) if self.legacy_profile_key else None)
                        or self.acp.profile
                    ),
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
                    model_context_window=(
                        meta.get("model_context_window") if isinstance(meta.get("model_context_window"), int) else None
                    ),
                    lineage=meta.get("lineage") if isinstance(meta.get("lineage"), dict) else {},
                    yolo=bool(meta.get("yolo", getattr(self.acp, "yolo", False))),
                )
                self.sessions[session_id] = session
            except Exception:
                logger.warning("Failed to load {} ACP session metadata {}", self.provider, meta_path)
        self._loaded = True

    def _write_meta(self, session: ACPSession) -> None:
        if session.meta_path is None:
            return
        session.meta_path.parent.mkdir(parents=True, exist_ok=True)
        payload = json.dumps(
            {
                "id": session.id,
                "provider": session.provider,
                "user_id": session.user_id,
                "provider_session_id": session.provider_session_id,
                "provider_profile": session.provider_profile,
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
                "yolo": session.yolo,
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
            elif block.get("type") == "resource" and isinstance(block.get("resource"), dict):
                resource = block["resource"]
                uri = resource.get("uri")
                if isinstance(uri, str) and uri:
                    parts.append(f"[Resource: {uri}]")
            elif block.get("type") in {"image", "audio", "resource_link"}:
                label = block.get("uri") or block.get("name") or block.get("type")
                parts.append(f"[{str(label)}]")
        return "\n".join(part for part in parts if part).strip()

    def _raw_preview(self, raw: dict) -> dict | None:
        encoded = json.dumps(raw, ensure_ascii=False, separators=(",", ":")).encode("utf-8", errors="replace")
        if len(encoded) <= RAW_PREVIEW_MAX_BYTES:
            return raw
        return {"type": raw.get("type"), "role": raw.get("role"), "omitted_bytes": len(encoded)}

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

    def _record_lineage_events(self, session: ACPSession, events: list[dict], start_index: int) -> None:
        try:
            from .agent_history import agent_history_store
        except Exception:
            return
        lineage = session.lineage
        for offset, event in enumerate(events):
            absolute_index = start_index + offset
            source_event_id = str(
                event.get("source_event_id")
                or f"{self.provider}:{session.provider_session_id}:{absolute_index}"
            )
            dispatch_id = lineage.get("driver_run_id") or lineage.get("query_message_id")
            if dispatch_id:
                source_event_id = f"dispatch:{dispatch_id}:{source_event_id}"
            raw = event.get("raw_preview") if isinstance(event.get("raw_preview"), dict) else {"source": self.provider}
            raw = {**raw, "chat_id": lineage.get("chat_id"), "streaming": bool(event.get("streaming", False))}
            event_type = event.get("event_type")
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
                source_event_id=source_event_id,
                source_line=None,
                role="assistant",
                event_type=str(event_type or "operation"),
                text=str(event.get("text") or ""),
                raw=raw,
                patch_text=event.get("patch_text") if isinstance(event.get("patch_text"), str) else None,
                file_changes=event.get("file_changes") if isinstance(event.get("file_changes"), list) else None,
            )
        if events:
            self._notify_lineage(session, events[-1])

    def _notify_lineage(self, session: ACPSession, event: dict[str, Any]) -> None:
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
            logger.debug("Could not notify Viewer about ACP stream update")

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

    def _snapshot(self, session: ACPSession, detail: str | None = None) -> dict:
        return {
            **session.summary(),
            "agent_capabilities": self.acp.capabilities_snapshot() if hasattr(self.acp, "capabilities_snapshot") else {},
            "prompts": session.prompts,
            "events": self._events_for_detail(session.events, normalize_agent_detail(detail)),
        }

    def snapshot(self, session_id: str, detail: str | None = None) -> dict:
        return self._snapshot(self.get(session_id), detail)

    def get(self, session_id: str) -> ACPSession:
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
        prompt_blocks = prompt if isinstance(prompt, list) else None
        session = ACPSession(
            provider=self.provider,
            id=session_id,
            user_id=normalized_user_id,
            provider_session_id=None,
            provider_profile=self.acp.profile,
            title=self._title_for(cleaned_prompt) if cleaned_prompt else f"New {self.provider} session",
            cwd=self._cwd_for(cwd, normalized_user_id),
            model=model.strip() if isinstance(model, str) and model.strip() else None,
            created_at=now,
            updated_at=now,
            status="idle",
            prompts=[{"text": cleaned_prompt, "content_blocks": prompt_blocks, "created_at": now}] if cleaned_prompt else [],
            meta_path=self._paths(session_id),
            lineage=dict(lineage or {}),
            yolo=bool(getattr(self.acp, "yolo", False)),
        )
        self.sessions[session_id] = session
        self._write_meta(session)
        if cleaned_prompt:
            session.acp_turn_index += 1
            session.status = "running"
            session.run_task = asyncio.create_task(self._run(session, prompt))
        return session.summary()

    async def send(self, session_id: str, prompt: str | list[dict[str, Any]], model: str | None = None, lineage: dict[str, str | None] | None = None) -> dict:
        session = self.get(session_id)
        cleaned_prompt = self._prompt_text(prompt)
        if not cleaned_prompt:
            raise HTTPException(status_code=400, detail="Prompt is required")
        if session.status == "running":
            raise HTTPException(status_code=409, detail=f"{self.provider} session is already running")
        if session.provider_session_id:
            # Validate/reload before accepting the turn. In particular, sessions
            # created by a retired transport may not be loadable by ACP. Raising
            # here lets the Super Workspace driver rotate
            # to a fresh Viewer/ACP session during the same dispatch instead of
            # returning success and failing later in the background task.
            session.loading_provider_history = True
            try:
                await self.acp.ensure_session(session.provider_session_id, session.cwd, model or session.model)
                # The ACP agent replays session history asynchronously AFTER
                # load_session returns (hermes schedules it via call_soon), so
                # replayed updates arrive while the guard below would already
                # be cleared. Drain until updates stop arriving for a short
                # quiet window (bounded) before dropping the guard.
                await self._drain_history_replay(session)
            finally:
                session.loading_provider_history = False
                session.events.clear()
                session.acp_events.clear()
                session.acp_event_keys.clear()
        now = time.time()
        session.prompts.append(
            {"text": cleaned_prompt, "content_blocks": prompt if isinstance(prompt, list) else None, "created_at": now}
        )
        if model:
            session.model = model
        if lineage is not None:
            session.lineage = dict(lineage)
        if not session.events:
            session.title = self._title_for(cleaned_prompt)
        session.status = "running"
        session.acp_turn_index += 1
        session.exit_code = None
        session.error = None
        session.updated_at = now
        self._write_meta(session)
        session.run_task = asyncio.create_task(self._run(session, prompt, session_loaded=True))
        return session.summary()

    async def _drain_history_replay(
        self,
        session: "ACPSession",
        quiet_window: float = 0.4,
        timeout: float = 15.0,
    ) -> None:
        """Wait until history-replay session updates stop arriving.

        ``_handle_acp_update`` touches ``session.updated_at`` even for updates
        swallowed by the loading guard, so a quiet ``updated_at`` means the
        replay stream has drained. Bounded so a chatty agent can never wedge
        dispatch; leftovers are still keyed by their own turn ids downstream.
        """
        deadline = time.monotonic() + timeout
        while True:
            marker = session.updated_at
            await asyncio.sleep(quiet_window)
            if session.updated_at == marker:
                return
            if time.monotonic() >= deadline:
                logger.warning(
                    "{} ACP history replay drain timed out session={} after {}s; proceeding anyway",
                    self.provider,
                    session.id,
                    timeout,
                )
                return

    async def _run(self, session: ACPSession, prompt: str | list[dict[str, Any]], session_loaded: bool = False) -> None:
        try:
            if session.provider_session_id and not session_loaded:
                await self.acp.ensure_session(session.provider_session_id, session.cwd, session.model)
            elif not session.provider_session_id:
                session.provider_session_id = await self.acp.new_session(session.cwd, session.model)
            self._write_meta(session)
            response = await self.acp.prompt(session.provider_session_id, prompt)
            self._finalize_acp_turn(session)
            stop_reason = getattr(response, "stop_reason", "end_turn")
            stop_value = getattr(stop_reason, "value", stop_reason)
            failed = str(stop_value) in {"refusal", "error"}
            session.status = "failed" if failed else "exited"
            session.exit_code = 1 if failed else 0
            session.error = f"{self.provider} ACP stopped with {stop_value}" if failed else None
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            # A provider may stream partial/final text before the ACP prompt
            # request itself fails. Finalize that message for persistence, but
            # keep the session failed: visible output is not proof that the
            # provider turn completed successfully.
            self._finalize_acp_turn(session)
            session.status = "failed"
            session.exit_code = 1
            session.error = str(exc) or exc.__class__.__name__
            session.updated_at = time.time()
            self._write_meta(session)
            logger.exception("{} ACP run failed session={} profile={}", self.provider, session.id, self.acp.profile)
        finally:
            session.updated_at = time.time()
            self._write_meta(session)
            session.run_task = None

    async def _handle_acp_update(self, provider_session_id: str, update: Any) -> None:
        self._ensure_loaded()
        session = next((item for item in self.sessions.values() if item.provider_session_id == provider_session_id), None)
        if session is None:
            return
        update_type = getattr(update, "session_update", "")
        raw = self._acp_dict(update)
        if update_type == "usage_update":
            used = getattr(update, "used", None)
            size = getattr(update, "size", None)
            if isinstance(used, int):
                session.total_tokens = used
            if isinstance(size, int) and size > 0:
                session.model_context_window = size
            if isinstance(size, int) and size > 0 and isinstance(used, int):
                session.updated_at = time.time()
        elif update_type == "session_info_update":
            title = getattr(update, "title", None)
            if isinstance(title, str) and title.strip():
                session.title = title.strip()
        elif update_type == "available_commands_update":
            session.available_commands = [
                self._acp_dict(value) for value in (getattr(update, "available_commands", None) or [])
            ]
        elif update_type == "current_mode_update":
            current_mode = getattr(update, "current_mode_id", None)
            session.current_mode = current_mode if isinstance(current_mode, str) else None
        elif update_type == "config_option_update":
            session.config_options = [
                self._acp_dict(value) for value in (getattr(update, "config_options", None) or [])
            ]

        if session.loading_provider_history:
            session.updated_at = time.time()
            self._write_meta(session)
            return

        event = self._normalized_acp_event(session, update_type, update, raw)
        if event is not None:
            index, changed = self._upsert_acp_event(session, event)
            if changed and session.lineage:
                self._record_lineage_events(session, [session.acp_events[index]], index)
            session.events = list(session.acp_events)
        session.updated_at = time.time()
        self._write_meta(session)

    @staticmethod
    def _acp_dict(value: Any) -> dict[str, Any]:
        if isinstance(value, dict):
            return value
        dump = getattr(value, "model_dump", None)
        if callable(dump):
            return dump(mode="json", by_alias=True, exclude_none=True)
        return {"value": str(value)}

    def _content_block_text(self, content: Any) -> str:
        raw = self._acp_dict(content)
        block_type = raw.get("type")
        if block_type == "text":
            return str(raw.get("text") or "")
        if block_type == "resource":
            resource = raw.get("resource") if isinstance(raw.get("resource"), dict) else {}
            return str(resource.get("text") or resource.get("uri") or "")
        if block_type in {"image", "audio", "resource_link"}:
            return f"[{raw.get('uri') or raw.get('name') or block_type}]"
        return ""

    def _normalized_acp_event(
        self,
        session: ACPSession,
        update_type: str,
        update: Any,
        raw: dict[str, Any],
    ) -> dict[str, Any] | None:
        now = time.time()
        event_type: AgentEventType
        event_key: str
        text = ""
        append = False
        patch_text: str | None = None
        file_changes: list[dict[str, Any]] = []

        if update_type in {"agent_message_chunk", "agent_thought_chunk"}:
            content = getattr(update, "content", None)
            text = self._content_block_text(content)
            if not text:
                return None
            # The provider stamps a per-turn UUID as messageId (hermes: live
            # turns mint one at prompt start; history replay stamps each old
            # message with its reconstructed turn id). A provider that omits
            # message ids gets a random uuid per message — chunks without a
            # stable id must never merge across turns, which is exactly the
            # bug the old acp_turn_index fallback caused.
            message_id = getattr(update, "message_id", None) or raw.get("messageId") or uuid.uuid4().hex
            event_key = f"{update_type}:{message_id}"
            event_type = AgentEventType.MESSAGE_ASSISTANT if update_type == "agent_message_chunk" else AgentEventType.REASONING
            append = True
        elif update_type in {"tool_call", "tool_call_update"}:
            tool_call_id = str(getattr(update, "tool_call_id", None) or raw.get("toolCallId") or "unknown")
            event_key = f"tool:{tool_call_id}"
            existing_index = session.acp_event_keys.get(event_key)
            existing_raw = (
                session.acp_events[existing_index].get("raw_preview", {})
                if existing_index is not None
                else {}
            )
            title = str(getattr(update, "title", None) or raw.get("title") or existing_raw.get("title") or "Tool call")
            status = str(getattr(update, "status", None) or raw.get("status") or "")
            raw_output = getattr(update, "raw_output", None)
            rows = [title]
            if status:
                rows.append(f"Status: {status}")
            if raw_output is not None and raw_output != "":
                rows.append(self._preview_text(raw_output))
            text = "\n".join(rows)
            event_type = AgentEventType.TOOL_CALL if update_type == "tool_call" else AgentEventType.TOOL_RESULT
            for item in getattr(update, "content", None) or []:
                item_raw = self._acp_dict(item)
                if item_raw.get("type") == "diff" and isinstance(item_raw.get("path"), str):
                    old_text = str(item_raw.get("oldText") or "")
                    new_text = str(item_raw.get("newText") or "")
                    patch_text = f"--- {item_raw['path']}\n+++ {item_raw['path']}\n-{old_text}\n+{new_text}"
                    file_changes.append({"path": item_raw["path"], "change_type": "edit", "diff": patch_text})
                elif item_raw.get("type") == "content":
                    content_text = self._content_block_text(item_raw.get("content"))
                    if content_text:
                        text = f"{text}\n{content_text}"
        elif update_type == "plan":
            event_key = f"plan:{session.acp_turn_index}"
            event_type = AgentEventType.PLAN_UPDATE
            entries = raw.get("entries") if isinstance(raw.get("entries"), list) else []
            symbols = {"completed": "✓", "in_progress": "●", "pending": "○"}
            text = "\n".join(
                f"{symbols.get(str(entry.get('status')), '○')} {entry.get('content')}"
                for entry in entries
                if isinstance(entry, dict) and entry.get("content")
            )
        elif update_type in {
            "usage_update",
            "session_info_update",
            "available_commands_update",
            "current_mode_update",
            "config_option_update",
        }:
            event_key = f"session:{update_type}"
            event_type = AgentEventType.SESSION_UPDATE
            text = self._preview_text(raw)
        else:
            return None

        if (append and text == "") or (not append and not text.strip()):
            return None
        return {
            "event_key": event_key,
            "source_event_id": f"{self.provider}-acp:{session.provider_session_id or session.id}:{event_key}",
            "received_at": now,
            "event_type": event_type,
            "text": text,
            "append": append,
            "file_changes": file_changes,
            "patch_text": patch_text,
            "raw_preview": self._raw_preview(raw),
            "streaming": append,
        }

    def _upsert_acp_event(self, session: ACPSession, event: dict[str, Any]) -> tuple[int, bool]:
        key = str(event.pop("event_key"))
        append = bool(event.pop("append", False))
        index = session.acp_event_keys.get(key)
        if index is None:
            index = len(session.acp_events)
            event["index"] = index
            session.acp_event_keys[key] = index
            session.acp_events.append(event)
            return index, True
        existing = session.acp_events[index]
        if append:
            event["text"] = f"{existing.get('text', '')}{event.get('text', '')}"
        elif key.startswith("tool:"):
            previous_raw = existing.get("raw_preview") if isinstance(existing.get("raw_preview"), dict) else {}
            next_raw = event.get("raw_preview") if isinstance(event.get("raw_preview"), dict) else {}
            event["raw_preview"] = {**previous_raw, **next_raw}
        event["index"] = index
        changed = event != existing
        session.acp_events[index] = event
        return index, changed

    def _finalize_acp_turn(self, session: ACPSession) -> None:
        index = next(
            (
                idx
                for idx in range(len(session.acp_events) - 1, -1, -1)
                if session.acp_events[idx].get("event_type") == AgentEventType.MESSAGE_ASSISTANT
                and session.acp_events[idx].get("streaming")
            ),
            None,
        )
        if index is None:
            return
        event = session.acp_events[index]
        if not event.get("streaming"):
            return
        event["streaming"] = False
        if session.lineage:
            self._record_lineage_events(session, [event], index)

    async def start(self) -> None:
        self._ensure_loaded()
        await self.acp.start()

    async def list_provider_sessions(self, cwd: str | None = None, cursor: str | None = None) -> dict[str, Any]:
        resolved_cwd = self._cwd_for(cwd, None) if cwd else None
        response = await self.acp.list_sessions(resolved_cwd, cursor)
        return self._acp_dict(response)

    async def fork(self, session_id: str, cwd: str | None = None) -> dict[str, Any]:
        source = self.get(session_id)
        if not source.provider_session_id:
            raise HTTPException(status_code=409, detail=f"{self.provider} session has not started")
        resolved_cwd = self._cwd_for(cwd, source.user_id) if cwd else source.cwd
        await self.acp.ensure_session(source.provider_session_id, source.cwd, source.model)
        provider_session_id = await self.acp.fork_session(source.provider_session_id, resolved_cwd)
        now = time.time()
        viewer_session_id = uuid.uuid4().hex
        forked = ACPSession(
            provider=self.provider,
            id=viewer_session_id,
            user_id=source.user_id,
            title=f"{source.title} (fork)",
            cwd=resolved_cwd,
            model=source.model,
            created_at=now,
            updated_at=now,
            provider_session_id=provider_session_id,
            provider_profile=self.acp.profile,
            status="exited",
            meta_path=self._paths(viewer_session_id),
            lineage=dict(source.lineage),
            yolo=bool(getattr(self.acp, "yolo", False)),
        )
        self.sessions[viewer_session_id] = forked
        self._write_meta(forked)
        return self._snapshot(forked, AGENT_DETAIL_FOCUS)

    async def resume(self, session_id: str) -> dict[str, Any]:
        session = self.get(session_id)
        if not session.provider_session_id:
            raise HTTPException(status_code=409, detail=f"{self.provider} session has not started")
        session.provider_session_id = await self.acp.resume_session(session.provider_session_id, session.cwd)
        session.status = "exited"
        session.error = None
        session.updated_at = time.time()
        self._write_meta(session)
        return self._snapshot(session, AGENT_DETAIL_FOCUS)

    async def set_mode(self, session_id: str, mode_id: str) -> dict[str, Any]:
        session = self.get(session_id)
        if not session.provider_session_id:
            raise HTTPException(status_code=409, detail=f"{self.provider} session has not started")
        await self.acp.ensure_session(session.provider_session_id, session.cwd, session.model)
        await self.acp.set_mode(session.provider_session_id, mode_id)
        session.current_mode = mode_id
        session.updated_at = time.time()
        self._write_meta(session)
        return self._snapshot(session, AGENT_DETAIL_FOCUS)

    async def terminate(self, session_id: str) -> dict:
        session = self.get(session_id)
        if session.provider_session_id:
            await self.acp.cancel(session.provider_session_id)
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
            session.error = f"{self.provider} ACP session was cancelled"
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
        await self.acp.shutdown()
