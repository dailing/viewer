from __future__ import annotations

from typing import Any
from urllib.parse import unquote

from fastapi import HTTPException

from .agent_history import SuperDriverRunCreate, agent_history_store
from .codex_sessions import codex_session_manager
from .files import read_workspaces
from .hermes_sessions import hermes_session_manager
from .super_workspace_runtime import super_workspace_runtime


class ConventionalWorkspaceAgentService:
    """Adapts traditional pane workspaces onto the Super Workspace driver queue."""

    def __init__(self) -> None:
        self._managers = {
            "codex": codex_session_manager,
            "hermes": hermes_session_manager,
        }

    @property
    def providers(self) -> tuple[str, ...]:
        return tuple(self._managers.keys())

    def manager(self, provider: str):
        manager = self._managers.get(provider)
        if manager is None:
            raise HTTPException(status_code=400, detail=f"Unsupported agent provider: {provider}")
        return manager

    def split_ref(self, ref: str) -> tuple[str, str]:
        cleaned = ref.strip()
        if not cleaned or ":" not in cleaned:
            raise HTTPException(status_code=400, detail="Agent session ref is invalid")
        provider, session_id = cleaned.split(":", 1)
        if provider not in self._managers or not session_id:
            raise HTTPException(status_code=400, detail="Agent session ref is invalid")
        return provider, session_id

    def active_workspace_id(self, user: str | None) -> str:
        return str(read_workspaces(user).active_workspace_id or "1")

    def list_sessions(self, provider: str | None, user: str | None) -> list[dict[str, Any]]:
        seen: set[str] = set()
        rows: list[dict[str, Any]] = []
        for role in agent_history_store.conventional_roles(user, provider):
            ref = str(role.session_ref or "")
            if not ref or ref in seen:
                continue
            seen.add(ref)
            session = self._session_summary_for_ref(ref, user)
            if session is None:
                session_provider, session_id = self.split_ref(ref)
                session = {
                    "id": session_id,
                    "title": str(role.name or ref),
                    "cwd": str(role.cwd or ""),
                    "cwd_relative": str(role.cwd or ""),
                    "model": role.model,
                    "created_at": float(role.created_at),
                    "updated_at": float(role.updated_at),
                    "status": "exited",
                    "exit_code": None,
                    "events": [],
                    "queue": [],
                    "provider": session_provider,
                }
            rows.append(session)
        return rows

    def _session_summary_for_ref(self, ref: str, user: str | None = None) -> dict[str, Any] | None:
        provider, session_id = self.split_ref(ref)
        try:
            return self.manager(provider).snapshot(session_id, "focus")
        except Exception:
            return None

    def attach_role(self, user: str | None, workspace_id: str, ref: str):
        provider, session_id = self.split_ref(ref)
        snapshot = self.manager(provider).snapshot(session_id, "focus")
        agent_history_store.upsert_conventional_role(
            user,
            workspace_id,
            session_ref=ref.strip(),
            provider=provider,
            name=str(snapshot.get("title") or ref),
            cwd=str(snapshot.get("cwd") or ""),
            model=snapshot.get("model") if isinstance(snapshot.get("model"), str) else None,
        )
        return read_workspaces(user)

    def remove_role(self, user: str | None, workspace_id: str, session_ref: str):
        agent_history_store.remove_conventional_role(user, workspace_id, unquote(session_ref).strip())
        return read_workspaces(user)

    def role_statuses(self, user: str | None, workspace_id: str):
        return agent_history_store.conventional_role_statuses(user, workspace_id)

    async def create_session(self, *, provider: str, prompt: str, cwd: str | None, model: str | None, user: str | None) -> dict[str, Any]:
        session = await self.manager(provider).create("", cwd, model, user)
        ref = f"{provider}:{session['id']}"
        agent_history_store.upsert_conventional_role(
            user,
            self.active_workspace_id(user),
            session_ref=ref,
            provider=provider,
            name=str(session.get("title") or ref),
            cwd=str(session.get("cwd") or ""),
            model=model,
        )
        if prompt.strip():
            await self.dispatch_turn(provider, session["id"], prompt, model, user)
        return self.manager(provider).snapshot(session["id"], "focus")

    async def dispatch_turn(self, provider: str, session_id: str, prompt: str, model: str | None, user: str | None) -> dict[str, Any]:
        cleaned_prompt = prompt.strip()
        if not cleaned_prompt:
            raise HTTPException(status_code=400, detail="Prompt is required")
        ref = f"{provider}:{session_id}"
        role = agent_history_store.conventional_role_for_ref(user, ref)
        if role is None:
            snapshot = self.manager(provider).snapshot(session_id, "focus")
            role = agent_history_store.upsert_conventional_role(
                user,
                self.active_workspace_id(user),
                session_ref=ref,
                provider=provider,
                name=str(snapshot.get("title") or ref),
                cwd=str(snapshot.get("cwd") or ""),
                model=model or snapshot.get("model"),
            )
        run = agent_history_store.create_super_run(
            user,
            cleaned_prompt,
            "queued",
            role_ids=[str(role.id)],
            raw={"type": "traditional_workspace_query_message", "session_ref": ref, "provider": provider},
            workspace_id=str(role.workspace_id),
        )
        agent_history_store.record_super_target(
            user,
            run.id,
            SuperDriverRunCreate(
                workspace_id=str(role.workspace_id),
                role_id=str(role.id),
                role_name=str(role.name or ref),
                provider=provider,
                session_ref=ref,
                parent_message_id=run.message_id,
                role_snapshot={
                    "id": str(role.id),
                    "name": str(role.name or ref),
                    "description": str(role.description or ""),
                    "provider": provider,
                    "cwd": str(role.cwd or ""),
                    "model": model or role.model,
                    "session_ref": ref,
                    "created_at": float(role.created_at),
                    "updated_at": float(role.updated_at),
                    "prompt_context": "traditional_workspace",
                },
            ),
        )
        await super_workspace_runtime.notify({"type": "run-created", "user_id": run.user_id, "run_id": run.id})
        return self.manager(provider).snapshot(session_id, "focus")


conventional_workspace_agents = ConventionalWorkspaceAgentService()
