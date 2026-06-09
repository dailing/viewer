from __future__ import annotations

import asyncio
import contextlib
import json
import os
import subprocess
import sys
import re
import time
from collections.abc import AsyncIterator
from pathlib import Path
from typing import Any
from uuid import uuid4
from urllib.error import URLError
from urllib.request import Request, urlopen

from fastapi import HTTPException
from loguru import logger
from pydantic import BaseModel
from sqlalchemy.exc import OperationalError

from .agent_history import SuperDispatchTask, SuperDriverRunCreate, SuperHistoryRun, agent_history_store
from .codex_sessions import codex_session_manager
from .hermes_sessions import hermes_session_manager
from .process_registry import process_slot_state, write_process_state
from .storage import LOG_DIR
from .super_workspace import SuperDispatchRequest, SuperRole, SuperRolePatch, super_workspace_manager
from .users import normalize_user_id


class SuperWorkspaceMessageCreate(BaseModel):
    message: str
    chat_id: str | None = None
    role_ids: list[str] | None = None
    parent_message_id: str | None = None
    sender_role_id: str | None = None


class SuperWorkspaceEventHub:
    def __init__(self) -> None:
        self._subscribers: set[asyncio.Queue[dict[str, Any]]] = set()

    async def publish(self, event: dict[str, Any]) -> None:
        dead: list[asyncio.Queue[dict[str, Any]]] = []
        for queue in self._subscribers:
            try:
                queue.put_nowait(event)
            except asyncio.QueueFull:
                dead.append(queue)
        for queue in dead:
            self._subscribers.discard(queue)
        if dead:
            logger.warning("Dropped {} slow Super Workspace subscriber(s)", len(dead))

    async def subscribe(self, user_id: str | None = None) -> AsyncIterator[str]:
        normalized_user = normalize_user_id(user_id)
        queue: asyncio.Queue[dict[str, Any]] = asyncio.Queue(maxsize=200)
        self._subscribers.add(queue)
        try:
            yield "event: ready\ndata: {}\n\n"
            while True:
                event = await queue.get()
                if event.get("user_id") not in {None, normalized_user}:
                    continue
                yield f"event: super-workspace\ndata: {json.dumps(event, ensure_ascii=False)}\n\n"
        finally:
            self._subscribers.discard(queue)


class SuperAgentDriver:
    provider = ""

    def active_session_id(self, role: SuperRole) -> str | None:
        if role.session_ref:
            provider, session_id = self.parse_ref(role.session_ref, role.provider)
            if provider == self.provider and session_id:
                try:
                    snapshot = self.manager().snapshot(session_id, "focus")
                    if snapshot.get("status") == "running":
                        return session_id
                    return None
                except Exception:
                    return None
        return None

    async def dispatch_task(self, role: SuperRole, user_id: str, prompt: str, lineage: dict[str, Any]) -> str:
        if role.session_ref:
            provider, session_id = self.parse_ref(role.session_ref, role.provider)
            if provider == self.provider and session_id:
                try:
                    snapshot = self.manager().snapshot(session_id, "focus")
                    if snapshot.get("status") == "running":
                        raise HTTPException(status_code=409, detail="Role session is already running")
                    await self._send(session_id, prompt, role.model, lineage)
                    return role.session_ref
                except HTTPException:
                    raise
                except Exception:
                    logger.warning("Super Workspace role {} has stale session_ref {}; creating a replacement", role.id, role.session_ref)
        first_prompt = f"{self.initial_prompt(role, user_id)}\n\n{prompt}"
        session = await self._create(first_prompt, role.cwd, role.model, user_id, lineage)
        session_ref = f"{self.provider}:{session['id']}"
        try:
            super_workspace_manager.update_role(role.id, SuperRolePatch(session_ref=session_ref), user_id)
        except HTTPException:
            logger.warning("Super Workspace role {} disappeared before session_ref could be saved", role.id)
        role.session_ref = session_ref
        return session_ref

    async def _create(self, prompt: str, cwd: str, model: str | None, user_id: str, lineage: dict[str, Any]) -> dict[str, Any]:
        if self.provider == "codex":
            return await self.manager().create(prompt, cwd, model, user_id, lineage=lineage)
        return await self.manager().create(prompt, cwd, model, user_id, lineage=lineage)

    async def _send(self, session_id: str, prompt: str, model: str | None, lineage: dict[str, Any]) -> dict[str, Any]:
        if self.provider == "codex":
            return await self.manager().send(session_id, prompt, model, lineage=lineage)
        return await self.manager().send(session_id, prompt, model, lineage=lineage)

    def manager(self):
        raise NotImplementedError

    def initial_prompt(self, role: SuperRole, user_id: str) -> str:
        data = super_workspace_manager.read(user_id)
        common = data.common_prompt.strip()
        role_prompt = (
            f'You are a persistent Super Workspace role named "{role.name}".\n\n'
            "Fixed role rules:\n"
            f"{role.description or '(No role rules were provided.)'}\n\n"
            "Operate as this role only. Prefer work that matches the fixed rules, files, topic, and responsibilities above. "
            "If a later user message appears unrelated to this role, say so briefly and ask for clarification instead of silently switching tasks."
        )
        return f"{common}\n\n{role_prompt}" if common else role_prompt

    @staticmethod
    def parse_ref(session_ref: str, fallback_provider: str) -> tuple[str, str]:
        if ":" in session_ref:
            provider, session_id = session_ref.split(":", 1)
            return provider or fallback_provider, session_id
        return fallback_provider, session_ref


class CodexSuperDriver(SuperAgentDriver):
    provider = "codex"

    def manager(self):
        return codex_session_manager


class HermesSuperDriver(SuperAgentDriver):
    provider = "hermes"

    def manager(self):
        return hermes_session_manager


class SuperWorkspaceRuntime:
    def __init__(self, notify_url: str | None = None) -> None:
        self.event_hub = SuperWorkspaceEventHub()
        self._worker_id = f"backend:{uuid4().hex}"
        self._notify_url = notify_url
        self._worker_task: asyncio.Task | None = None
        self._active_tasks: set[asyncio.Task] = set()
        self._stop = asyncio.Event()
        self._drivers: dict[str, SuperAgentDriver] = {
            "codex": CodexSuperDriver(),
            "hermes": HermesSuperDriver(),
        }

    async def start(self) -> None:
        self._stop.clear()
        self.ensure_worker_process()
        logger.info("Super Workspace runtime started")

    async def shutdown(self) -> None:
        self._stop.set()
        if self._worker_task is not None:
            self._worker_task.cancel()
            await asyncio.gather(self._worker_task, return_exceptions=True)
        for task in list(self._active_tasks):
            task.cancel()
        if self._active_tasks:
            await asyncio.gather(*self._active_tasks, return_exceptions=True)
        self._active_tasks.clear()

    def ensure_worker_process(self) -> None:
        name = "worker"
        slot = process_slot_state(name)
        if slot["pid_file_exists"] and slot["alive"]:
            logger.error(
                "Super Workspace worker pid file already points to a live process; not starting duplicate pid={} pid_file={}",
                slot["pid"],
                slot["pid_path"],
            )
            return
        if slot["pid_file_exists"]:
            logger.warning(
                "Stale Super Workspace worker pid file found; overwriting pid={} pid_file={}",
                slot["pid"],
                slot["pid_path"],
            )
        log_path = LOG_DIR / "super-workspace-worker.log"
        log_path.parent.mkdir(parents=True, exist_ok=True)
        project_root = Path(__file__).resolve().parents[2]
        env = dict(os.environ)
        env.setdefault("VIEWER_SUPER_WORKSPACE_NOTIFY_URL", self._default_notify_url())
        with log_path.open("ab") as log:
            process = subprocess.Popen(
                [sys.executable, "-m", "backend.app.super_workspace_worker"],
                stdin=subprocess.DEVNULL,
                stdout=log,
                stderr=log,
                cwd=project_root,
                env=env,
                start_new_session=True,
            )
        write_process_state(name, process.pid, {"kind": "super_workspace_worker", "log_path": log_path.as_posix()})
        logger.info("Started Super Workspace worker pid={} log={}", process.pid, log_path)

    def _default_notify_url(self) -> str:
        port = os.environ.get("VIEWER_PORT", "8000")
        return f"http://127.0.0.1:{port}/internal/super-workspace/notify"

    async def notify(self, event: dict[str, Any]) -> None:
        await self._emit_update(event)

    async def _emit_update(self, event: dict[str, Any]) -> None:
        await self.event_hub.publish(event)
        if self._notify_url:
            await asyncio.to_thread(self._post_notify, event)

    def _post_notify(self, event: dict[str, Any]) -> None:
        body = json.dumps(event, ensure_ascii=False).encode("utf-8")
        request = Request(self._notify_url, data=body, headers={"Content-Type": "application/json"}, method="POST")
        try:
            with urlopen(request, timeout=2.0) as response:
                response.read()
        except (OSError, URLError):
            pass

    async def submit(self, request: SuperWorkspaceMessageCreate, user_id: str | None = None) -> SuperHistoryRun:
        message = request.message.strip()
        if not message:
            raise HTTPException(status_code=400, detail="Message is required")
        normalized_user = normalize_user_id(user_id)
        data = super_workspace_manager.read(normalized_user)
        chat = super_workspace_manager.active_chat(normalized_user, request.chat_id)
        chat_role_ids = set(chat.member_role_ids)
        scoped_roles = [role for role in data.roles if role.id in chat_role_ids]
        parsed_role_ids, citation_ids, query = self._parse_query_prefix(message, data.roles)
        role_ids = self._merge_requested_role_ids(request.role_ids or [], parsed_role_ids, data.roles)
        if not query:
            raise HTTPException(status_code=400, detail="Query is required")
        if not role_ids and not scoped_roles:
            raise HTTPException(status_code=400, detail="Assign at least one role to this chat before dispatching")
        try:
            run = agent_history_store.create_super_run(
                normalized_user,
                query,
                "selecting",
                role_ids,
                citation_ids=citation_ids,
                parent_message_id=request.parent_message_id,
                sender_role_id=request.sender_role_id,
                chat_id=chat.id,
            )
        except ValueError as exc:
            raise HTTPException(status_code=400, detail=str(exc)) from exc
        await self._emit_update({"type": "run-created", "user_id": normalized_user, "chat_id": run.chat_id, "run_id": run.id})
        rationale = ""
        if not role_ids:
            try:
                dispatch = await super_workspace_manager.dispatch(
                    SuperDispatchRequest(message=query, role_ids=[role.id for role in scoped_roles]),
                    normalized_user,
                )
                role_ids = self._expand_role_names(dispatch.role_ids, scoped_roles)
                rationale = dispatch.rationale
            except HTTPException as exc:
                failed = agent_history_store.update_super_run(run.id, normalized_user, status="failed", error=str(exc.detail))
                await self._emit_update({"type": "run-updated", "user_id": normalized_user, "chat_id": run.chat_id, "run_id": run.id})
                return failed
        queued = agent_history_store.update_super_run(run.id, normalized_user, status="queued", role_ids=role_ids, rationale=rationale)
        roles_by_id = {role.id: role for role in data.roles}
        for role_id in role_ids:
            role = roles_by_id.get(role_id)
            if role is None:
                continue
            agent_history_store.record_super_target(
                normalized_user,
                run.id,
                SuperDriverRunCreate(
                    workspace_id=run.workspace_id,
                    chat_id=run.chat_id,
                    role_id=role.id,
                    role_name=role.name,
                    provider=role.provider or "codex",
                    parent_message_id=run.parent_message_id or run.message_id,
                    sender_role_id=run.sender_role_id,
                    role_snapshot=role.model_dump(),
                ),
            )
        await self._emit_update({"type": "run-updated", "user_id": normalized_user, "chat_id": run.chat_id, "run_id": run.id})
        return agent_history_store.get_super_run(queued.id, normalized_user)

    def _merge_requested_role_ids(self, requested_role_ids: list[str], parsed_role_ids: list[str], roles: list[SuperRole]) -> list[str]:
        valid_role_ids = {role.id for role in roles}
        merged: list[str] = []
        for role_id in [*requested_role_ids, *parsed_role_ids]:
            if role_id not in valid_role_ids or role_id in merged:
                continue
            merged.append(role_id)
        return merged

    def _parse_query_prefix(self, message: str, roles: list[SuperRole]) -> tuple[list[str], list[str], str]:
        by_key: dict[str, list[SuperRole]] = {}
        for role in roles:
            by_key.setdefault(self.mention_key(role), []).append(role)
        role_ids: list[str] = []
        citation_ids: list[str] = []
        position = 0
        while position < len(message) and message[position] == "@":
            match = re.match(r"@(\S+)(?:\s+|$)", message[position:])
            if not match:
                break
            token = match.group(1)
            matched_roles = by_key.get(token)
            if matched_roles:
                for role in matched_roles:
                    if role.id not in role_ids:
                        role_ids.append(role.id)
            else:
                citation_match = re.fullmatch(r"(?:msg|message)-([A-Za-z0-9_.:-]+)", token)
                if not citation_match:
                    raise HTTPException(status_code=400, detail=f"Unknown Super Workspace mention: @{token}")
                message_id = citation_match.group(1)
                if message_id not in citation_ids:
                    citation_ids.append(message_id)
            position += match.end()
        return role_ids, citation_ids, message[position:].strip()

    def _expand_role_names(self, role_ids: list[str], roles: list[SuperRole]) -> list[str]:
        by_id = {role.id: role for role in roles}
        selected_names = {by_id[role_id].name for role_id in role_ids if role_id in by_id}
        expanded: list[str] = []
        for role in roles:
            if role.name in selected_names and role.id not in expanded:
                expanded.append(role.id)
        return expanded

    def mention_key(self, role: SuperRole) -> str:
        parts = re.findall(r"[A-Za-z_][A-Za-z0-9_]*|[0-9]+", role.name.strip())
        value = "_".join(parts).strip("_")
        if not value:
            value = role.id
        if not re.match(r"^[A-Za-z_]", value):
            value = f"_{value}"
        return value

    async def _dispatch_worker_loop(self) -> None:
        logger.info("Super Workspace DB dispatch worker started id={}", self._worker_id)
        while not self._stop.is_set():
            self._active_tasks = {task for task in self._active_tasks if not task.done()}
            while len(self._active_tasks) < 4:
                try:
                    task = agent_history_store.claim_next_dispatch_task(self._worker_id)
                except OperationalError as exc:
                    if self._is_database_locked(exc):
                        logger.warning("Super Workspace dispatch worker waiting for agent-history DB lock")
                        await asyncio.sleep(1.0)
                        break
                    raise
                if task is None:
                    break
                worker = asyncio.create_task(self._process_dispatch_task(task))
                self._active_tasks.add(worker)
            await asyncio.sleep(0.5 if self._active_tasks else 1.0)

    async def _process_dispatch_task(self, task: SuperDispatchTask) -> None:
        try:
            await self._dispatch_task(task)
        except OperationalError as exc:
            if self._is_database_locked(exc):
                logger.warning("Super Workspace dispatch task waiting for agent-history DB lock task={} role={}", task.id, task.role_id)
                with contextlib.suppress(Exception):
                    agent_history_store.update_driver_run_status(task.id, "queued", next_attempt_at=time.time() + 1.0)
                await asyncio.sleep(1.0)
                return
            raise
        except Exception as exc:
            logger.exception("Super Workspace dispatch task failed task={} role={}", task.id, task.role_id)
            agent_history_store.update_driver_run_status(task.id, "failed", error=str(exc) or "Dispatch task failed")
            agent_history_store.summarize_super_run_status(task.query_message_id, task.user_id, fallback_error=str(exc))
            await self._emit_update({"type": "run-updated", "user_id": task.user_id, "run_id": task.query_message_id})

    @staticmethod
    def _is_database_locked(exc: OperationalError) -> bool:
        return "database is locked" in str(exc).lower()

    async def _dispatch_task(self, task: SuperDispatchTask) -> None:
        run = agent_history_store.get_super_run(task.query_message_id, task.user_id)
        role = self._role_for_task(task)
        driver = self._drivers.get(role.provider or "codex")
        if driver is None:
            agent_history_store.update_driver_run_status(task.id, "failed", error=f"Unsupported provider: {role.provider}")
            agent_history_store.summarize_super_run_status(run.id, task.user_id, fallback_error=f"Unsupported provider: {role.provider}")
            return
        active_session_id = driver.active_session_id(role)
        if active_session_id is not None:
            agent_history_store.update_driver_run_status(task.id, "queued", next_attempt_at=time.time() + 2.0)
            await self._emit_update({"type": "run-updated", "user_id": task.user_id, "chat_id": run.chat_id, "run_id": run.id})
            return
        prompt = self.role_message_prompt(run, role)
        lineage = {
            "workspace_id": task.workspace_id,
            "chat_id": run.chat_id,
            "query_message_id": run.message_id,
            "driver_run_id": task.id,
            "parent_message_id": task.parent_message_id or run.message_id,
            "sender_role_id": task.sender_role_id,
            "recipient_role_id": role.id,
            "role_id": role.id,
            "role_name": role.name,
        }
        agent_history_store.update_super_run(run.id, task.user_id, status="running")
        agent_history_store.update_driver_run_status(task.id, "running", agent_prompt=prompt, error="")
        await self._emit_update({"type": "run-updated", "user_id": task.user_id, "chat_id": run.chat_id, "run_id": run.id})
        session_ref = await driver.dispatch_task(role, task.user_id, prompt, lineage)
        agent_history_store.update_driver_run_status(task.id, "running", session_ref=session_ref, agent_prompt=prompt)
        provider, session_id = driver.parse_ref(session_ref, role.provider)
        completed = await self._wait_for_session(driver, session_id, task.user_id, run.id, task.id)
        await self._emit_update(
            {
                "type": "run-updated",
                "user_id": task.user_id,
                "chat_id": run.chat_id,
                "run_id": run.id,
                "status": completed.status,
                "updated_at": time.time(),
            }
        )

    def _role_for_task(self, task: SuperDispatchTask) -> SuperRole:
        data = super_workspace_manager.read(task.user_id)
        current = next((candidate for candidate in data.roles if candidate.id == task.role_id), None)
        raw = dict(task.role_snapshot)
        if current is not None:
            raw["session_ref"] = current.session_ref or raw.get("session_ref") or ""
        raw.setdefault("id", task.role_id)
        raw.setdefault("name", task.role_name)
        raw.setdefault("provider", task.provider or "codex")
        raw.setdefault("description", "")
        raw.setdefault("cwd", "")
        raw.setdefault("model", None)
        now = time.time()
        raw.setdefault("created_at", now)
        raw.setdefault("updated_at", now)
        return SuperRole.model_validate(raw)

    async def _wait_for_session(self, driver: SuperAgentDriver, session_id: str, user_id: str, run_id: str, driver_run_id: str) -> SuperHistoryRun:
        manager = driver.manager()
        provider = driver.provider
        while not self._stop.is_set():
            snapshot = manager.snapshot(session_id, "focus")
            queue = snapshot.get("queue") if isinstance(snapshot.get("queue"), list) else []
            status = str(snapshot.get("status") or "")
            if driver_run_id:
                agent_history_store.update_driver_run_status(driver_run_id, "running")
            await self._emit_update(
                {
                    "type": "run-updated",
                    "user_id": user_id,
                    "run_id": run_id,
                    "session_id": session_id,
                    "provider": provider,
                    "status": status,
                    "updated_at": time.time(),
                }
            )
            if status not in {"running"} and not queue:
                if status == "failed":
                    if driver_run_id and self._driver_has_final_response(run_id, user_id, driver_run_id):
                        agent_history_store.update_driver_run_status(driver_run_id, "completed")
                        return self._summarize_run_status(run_id, user_id)
                    if driver_run_id:
                        agent_history_store.update_driver_run_status(driver_run_id, "failed")
                    return self._summarize_run_status(run_id, user_id, fallback_error="Role session failed")
                if driver_run_id:
                    agent_history_store.update_driver_run_status(driver_run_id, "completed")
                return self._summarize_run_status(run_id, user_id)
            await asyncio.sleep(1.0)
        return agent_history_store.update_super_run(run_id, user_id, status="running")

    def _summarize_run_status(self, run_id: str, user_id: str, fallback_error: str = "") -> SuperHistoryRun:
        run = agent_history_store.get_super_run(run_id, user_id)
        statuses: list[str] = []
        for target in run.targets:
            if target.status == "failed" and self._target_has_final_response(target):
                agent_history_store.update_driver_run_status(target.id, "completed")
                statuses.append("completed")
            else:
                statuses.append(target.status)
        if any(status == "failed" for status in statuses):
            return agent_history_store.update_super_run(run_id, user_id, status="failed", error=fallback_error)
        if statuses and all(status == "completed" for status in statuses):
            return agent_history_store.update_super_run(run_id, user_id, status="completed")
        return agent_history_store.update_super_run(run_id, user_id, status="running")

    def _driver_has_final_response(self, run_id: str, user_id: str, driver_run_id: str) -> bool:
        run = agent_history_store.get_super_run(run_id, user_id)
        target = next((item for item in run.targets if item.id == driver_run_id), None)
        return self._target_has_final_response(target) if target else False

    @staticmethod
    def _target_has_final_response(target: Any) -> bool:
        return any(
            message.role == "assistant"
            and message.event_type == "message:assistant"
            and message.text.strip()
            for message in target.messages
        )

    def role_message_prompt(self, run: SuperHistoryRun, role: SuperRole) -> str:
        lineage = {
            "workspace_id": run.workspace_id,
            "chat_id": run.chat_id,
            "run_id": run.id,
            "message_id": run.message_id,
            "parent_message_id": run.parent_message_id,
            "sender_role_id": run.sender_role_id,
            "recipient_role_id": role.id,
        }
        cited_messages = agent_history_store.cited_messages_for_query(run.user_id, run.message_id)
        cited_section = self._citation_prompt_section(cited_messages)
        query_heading = "The below is the query for this time:" if cited_section else "User message:"
        if ":workspace:" in run.workspace_id:
            intro = "The traditional workspace routed this message to this pane's agent session. Continue the existing session context and answer the user request."
        else:
            intro = "Super Workspace routed this message to your role. Apply your fixed role rules and answer only for your own responsibility."
        return (
            f"{intro}\n\n"
            f"Routing metadata:\n{json.dumps(lineage, ensure_ascii=False)}\n\n"
            f"{self._chat_prompt_section(run)}"
            f"{cited_section}"
            f"{query_heading}\n{run.message}"
        )

    def _chat_prompt_section(self, run: SuperHistoryRun) -> str:
        chat = super_workspace_manager.chat_for_run(run.user_id, run.workspace_id, run.chat_id)
        prompt = chat.common_prompt.strip()
        if not prompt:
            return ""
        return f"Chat-level instructions:\n{prompt}\n\n"

    def _citation_prompt_section(self, messages: list[Any]) -> str:
        if not messages:
            return ""
        blocks: list[str] = ["The user cited the following Super Workspace message(s) for context:"]
        for index, message in enumerate(messages, start=1):
            content = (message.query or message.text or "").strip()
            if not content:
                content = "(No visible focus-mode text.)"
            metadata = {
                "message_id": message.id,
                "provider": message.provider,
                "role": message.role,
                "event_type": message.event_type,
                "occurred_at": message.occurred_at,
                "sender_role_id": message.sender_role_id,
                "recipient_role_id": message.recipient_role_id,
            }
            blocks.append(
                f"[{index}] Metadata:\n{json.dumps(metadata, ensure_ascii=False)}\n\n"
                f"Message:\n{content}"
            )
        return "\n\n".join(blocks) + "\n\n"


super_workspace_runtime = SuperWorkspaceRuntime()
