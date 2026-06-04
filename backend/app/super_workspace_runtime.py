from __future__ import annotations

import asyncio
import json
import re
import time
from collections.abc import AsyncIterator
from dataclasses import dataclass
from typing import Any

from fastapi import HTTPException
from loguru import logger
from pydantic import BaseModel

from .agent_history import SuperDriverRunCreate, SuperHistoryRun, agent_history_store
from .codex_sessions import codex_session_manager
from .hermes_sessions import hermes_session_manager
from .super_workspace import SuperDispatchRequest, SuperRole, SuperRolePatch, super_workspace_manager
from .users import normalize_user_id


class SuperWorkspaceMessageCreate(BaseModel):
    message: str
    parent_message_id: str | None = None
    sender_role_id: str | None = None


@dataclass
class SuperQueuedRun:
    run_id: str
    user_id: str
    role_id: str


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

    async def ensure_session(self, role: SuperRole, user_id: str) -> str:
        if role.session_ref:
            provider, session_id = self.parse_ref(role.session_ref, role.provider)
            if provider == self.provider and session_id:
                try:
                    self.manager().snapshot(session_id, "focus")
                    return role.session_ref
                except Exception:
                    logger.warning("Super Workspace role {} has stale session_ref {}; creating a replacement", role.id, role.session_ref)
        session = await self.create_session(role, user_id)
        session_ref = f"{self.provider}:{session['id']}"
        super_workspace_manager.update_role(role.id, SuperRolePatch(session_ref=session_ref), user_id)
        role.session_ref = session_ref
        return session_ref

    async def dispatch(self, role: SuperRole, user_id: str, prompt: str) -> dict[str, Any]:
        session_ref = await self.ensure_session(role, user_id)
        _, session_id = self.parse_ref(session_ref, role.provider)
        snapshot = self.manager().snapshot(session_id, "focus")
        if snapshot.get("status") == "running":
            return await self.manager().enqueue(session_id, prompt, role.model)
        return await self.manager().send(session_id, prompt, role.model)

    async def create_session(self, role: SuperRole, user_id: str) -> dict[str, Any]:
        return await self.manager().create(self.initial_prompt(role, user_id), role.cwd, role.model, user_id)

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
    def __init__(self) -> None:
        self.event_hub = SuperWorkspaceEventHub()
        self._queues: dict[str, asyncio.Queue[SuperQueuedRun]] = {}
        self._workers: dict[str, asyncio.Task] = {}
        self._stop = asyncio.Event()
        self._drivers: dict[str, SuperAgentDriver] = {
            "codex": CodexSuperDriver(),
            "hermes": HermesSuperDriver(),
        }

    async def start(self) -> None:
        self._stop.clear()
        logger.info("Super Workspace runtime started")

    async def shutdown(self) -> None:
        self._stop.set()
        for task in self._workers.values():
            task.cancel()
        if self._workers:
            await asyncio.gather(*self._workers.values(), return_exceptions=True)
        self._workers.clear()
        self._queues.clear()

    async def submit(self, request: SuperWorkspaceMessageCreate, user_id: str | None = None) -> SuperHistoryRun:
        message = request.message.strip()
        if not message:
            raise HTTPException(status_code=400, detail="Message is required")
        normalized_user = normalize_user_id(user_id)
        data = super_workspace_manager.read(normalized_user)
        role_ids, query = self._parse_query_prefix(message, data.roles)
        if not query:
            raise HTTPException(status_code=400, detail="Query is required")
        run = agent_history_store.create_super_run(
            normalized_user,
            query,
            "selecting",
            role_ids,
            parent_message_id=request.parent_message_id,
            sender_role_id=request.sender_role_id,
        )
        await self.event_hub.publish({"type": "run-created", "user_id": normalized_user, "run_id": run.id})
        rationale = ""
        if not role_ids:
            try:
                dispatch = await super_workspace_manager.dispatch(SuperDispatchRequest(message=query), normalized_user)
                role_ids = dispatch.role_ids
                rationale = dispatch.rationale
            except HTTPException as exc:
                failed = agent_history_store.update_super_run(run.id, normalized_user, status="failed", error=str(exc.detail))
                await self.event_hub.publish({"type": "run-updated", "user_id": normalized_user, "run_id": run.id})
                return failed
        queued = agent_history_store.update_super_run(run.id, normalized_user, status="queued", role_ids=role_ids, rationale=rationale)
        for role_id in role_ids:
            self._enqueue(SuperQueuedRun(run_id=run.id, user_id=normalized_user, role_id=role_id))
        await self.event_hub.publish({"type": "run-updated", "user_id": normalized_user, "run_id": run.id})
        return queued

    def _parse_query_prefix(self, message: str, roles: list[SuperRole]) -> tuple[list[str], str]:
        by_key = {self.mention_key(role): role for role in roles}
        role_ids: list[str] = []
        position = 0
        while position < len(message) and message[position] == "@":
            match = re.match(r"@([A-Za-z_][A-Za-z0-9_]*) ", message[position:])
            if not match:
                break
            key = match.group(1)
            role = by_key.get(key)
            if role is None:
                raise HTTPException(status_code=400, detail=f"Unknown Super Workspace role mention: @{key}")
            if role.id not in role_ids:
                role_ids.append(role.id)
            position += match.end()
        return role_ids, message[position:].strip()

    def mention_key(self, role: SuperRole) -> str:
        parts = re.findall(r"[A-Za-z_][A-Za-z0-9_]*|[0-9]+", role.name.strip())
        value = "_".join(parts).strip("_")
        if not value:
            value = role.id
        if not re.match(r"^[A-Za-z_]", value):
            value = f"_{value}"
        return value

    def _enqueue(self, item: SuperQueuedRun) -> None:
        queue = self._queues.setdefault(item.role_id, asyncio.Queue())
        queue.put_nowait(item)
        if item.role_id not in self._workers or self._workers[item.role_id].done():
            self._workers[item.role_id] = asyncio.create_task(self._role_worker(item.role_id))

    async def _role_worker(self, role_id: str) -> None:
        queue = self._queues[role_id]
        while not self._stop.is_set():
            item = await queue.get()
            try:
                await self._dispatch_role(item)
            except Exception:
                logger.exception("Super Workspace role dispatch failed role={} run={}", item.role_id, item.run_id)
                try:
                    agent_history_store.update_super_run(item.run_id, item.user_id, status="failed", error="Role dispatch failed")
                    await self.event_hub.publish({"type": "run-updated", "user_id": item.user_id, "run_id": item.run_id})
                except Exception:
                    logger.exception("Failed to mark Super Workspace run failed")
            finally:
                queue.task_done()

    async def _dispatch_role(self, item: SuperQueuedRun) -> None:
        run = agent_history_store.get_super_run(item.run_id, item.user_id)
        data = super_workspace_manager.read(item.user_id)
        role = next((candidate for candidate in data.roles if candidate.id == item.role_id), None)
        if role is None:
            agent_history_store.update_super_run(run.id, item.user_id, status="failed", error=f"Role not found: {item.role_id}")
            return
        driver = self._drivers.get(role.provider or "codex")
        if driver is None:
            agent_history_store.update_super_run(run.id, item.user_id, status="failed", error=f"Unsupported provider: {role.provider}")
            return
        agent_history_store.update_super_run(run.id, item.user_id, status="running")
        await self.event_hub.publish({"type": "run-updated", "user_id": item.user_id, "run_id": run.id})
        prompt = self.role_message_prompt(run, role)
        session_ref = await driver.ensure_session(role, item.user_id)
        target_request = SuperDriverRunCreate(
            role_id=role.id,
            role_name=role.name,
            provider=role.provider or driver.provider,
            session_ref=session_ref,
            agent_prompt=prompt,
            parent_message_id=run.parent_message_id or run.message_id,
            sender_role_id=run.sender_role_id,
        )
        recorded = agent_history_store.record_super_target(item.user_id, run.id, target_request)
        driver_run_id = ""
        for target in recorded.targets:
            if target.role_id == role.id and target.session_ref == session_ref:
                driver_run_id = target.id
                break
        await driver.dispatch(role, item.user_id, prompt)
        provider, session_id = driver.parse_ref(session_ref, role.provider)
        completed = await self._wait_for_session(provider, session_id, item.user_id, run.id, driver_run_id)
        await self.event_hub.publish(
            {
                "type": "run-updated",
                "user_id": item.user_id,
                "run_id": run.id,
                "status": completed.status,
                "updated_at": time.time(),
            }
        )

    async def _wait_for_session(self, provider: str, session_id: str, user_id: str, run_id: str, driver_run_id: str) -> SuperHistoryRun:
        manager = self._drivers[provider].manager()
        while not self._stop.is_set():
            agent_history_store.sync_session(provider, session_id, user_id)
            snapshot = manager.snapshot(session_id, "focus")
            queue = snapshot.get("queue") if isinstance(snapshot.get("queue"), list) else []
            status = str(snapshot.get("status") or "")
            await self.event_hub.publish(
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
        statuses = [target.status for target in run.targets]
        if any(status == "failed" for status in statuses):
            return agent_history_store.update_super_run(run_id, user_id, status="failed", error=fallback_error)
        if statuses and all(status == "completed" for status in statuses):
            return agent_history_store.update_super_run(run_id, user_id, status="completed")
        return agent_history_store.update_super_run(run_id, user_id, status="running")

    def role_message_prompt(self, run: SuperHistoryRun, role: SuperRole) -> str:
        lineage = {
            "run_id": run.id,
            "message_id": run.message_id,
            "parent_message_id": run.parent_message_id,
            "sender_role_id": run.sender_role_id,
            "recipient_role_id": role.id,
        }
        return (
            "Super Workspace routed this message to your role. Apply your fixed role rules and answer only for your own responsibility.\n\n"
            f"Routing metadata:\n{json.dumps(lineage, ensure_ascii=False)}\n\n"
            f"User message:\n{run.message}"
        )


super_workspace_runtime = SuperWorkspaceRuntime()
