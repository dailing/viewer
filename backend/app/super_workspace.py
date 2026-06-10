import asyncio
import json
import os
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from fastapi import HTTPException
from pydantic import BaseModel, Field

from .agent_history import DEFAULT_SUPER_WORKSPACE_ID, DEFAULT_SUPER_WORKSPACE_NAME, SuperChatList, SuperChatSummary, SuperWorkspaceList, agent_history_store


DEFAULT_DISPATCH_MODEL = "deepseek-v4-flash"
DEFAULT_DISPATCH_URL = "https://api.deepseek.com/v1/chat/completions"
PROJECT_ENV_PATH = Path(__file__).resolve().parents[2] / ".viewer.env"


class SuperRole(BaseModel):
    id: str
    name: str
    description: str = ""
    provider: str = "codex"
    cwd: str = ""
    model: str | None = None
    session_policy: str = "reuse"
    created_at: float
    updated_at: float


class SuperRoleCreate(BaseModel):
    name: str
    description: str = ""
    provider: str = "codex"
    cwd: str = ""
    model: str | None = None
    session_policy: str = "reuse"


class SuperRolePatch(BaseModel):
    name: str | None = None
    description: str | None = None
    provider: str | None = None
    cwd: str | None = None
    model: str | None = None
    session_policy: str | None = None


class SuperWorkspaceData(BaseModel):
    id: str = DEFAULT_SUPER_WORKSPACE_ID
    name: str = DEFAULT_SUPER_WORKSPACE_NAME
    common_prompt: str = ""
    roles: list[SuperRole] = Field(default_factory=list)


class SuperWorkspacePatch(BaseModel):
    common_prompt: str | None = None


class SuperChatCreate(BaseModel):
    name: str = "New Chat"
    type: str = "group"
    pinned: bool = False
    cwd: str = ""
    common_prompt: str = ""
    member_role_ids: list[str] = Field(default_factory=list)


class SuperChatPatch(BaseModel):
    name: str | None = None
    type: str | None = None
    pinned: bool | None = None
    cwd: str | None = None
    common_prompt: str | None = None
    member_role_ids: list[str] | None = None


class SuperDispatchRequest(BaseModel):
    message: str
    role_ids: list[str] | None = None


class SuperDispatchResponse(BaseModel):
    role_ids: list[str]
    rationale: str = ""
    raw: dict[str, Any] | None = None


class SuperWorkspaceManager:
    def list_workspaces(self, user_id: str | None = None) -> SuperWorkspaceList:
        return agent_history_store.list_super_workspaces(user_id)

    def activate_workspace(self, workspace_id: str, user_id: str | None = None) -> SuperWorkspaceList:
        try:
            return agent_history_store.activate_super_workspace(user_id, workspace_id)
        except KeyError as exc:
            raise HTTPException(status_code=404, detail="Super Workspace not found") from exc

    def list_chats(self, user_id: str | None = None) -> SuperChatList:
        return agent_history_store.list_super_chats(user_id)

    def active_chat(self, user_id: str | None = None, chat_id: str | None = None) -> SuperChatSummary:
        try:
            if chat_id:
                active_workspace = agent_history_store.active_super_workspace(user_id)
                chats = agent_history_store.list_super_chats(user_id, active_workspace.id)
                selected = next((chat for chat in chats.chats if chat.id == chat_id), None)
                if selected is None:
                    raise KeyError(chat_id)
                return selected
            else:
                chats = agent_history_store.list_super_chats(user_id)
        except KeyError as exc:
            raise HTTPException(status_code=404, detail="Chat not found") from exc
        active = next((chat for chat in chats.chats if chat.id == chats.active_chat_id), None)
        if active is None:
            raise HTTPException(status_code=404, detail="Chat not found")
        return active

    def chat_for_run(self, user_id: str | None, workspace_id: str, chat_id: str) -> SuperChatSummary:
        try:
            chats = agent_history_store.list_super_chats(user_id, workspace_id)
        except KeyError as exc:
            raise HTTPException(status_code=404, detail="Chat not found") from exc
        active = next((chat for chat in chats.chats if chat.id == chat_id), None)
        if active is None:
            raise HTTPException(status_code=404, detail="Chat not found")
        return active

    def create_chat(self, request: SuperChatCreate, user_id: str | None = None) -> SuperChatList:
        try:
            return agent_history_store.create_super_chat(
                user_id,
                name=request.name,
                chat_type=request.type,
                pinned=request.pinned,
                common_prompt=request.common_prompt,
                member_role_ids=request.member_role_ids,
                cwd=request.cwd,
            )
        except ValueError as exc:
            raise HTTPException(status_code=400, detail=str(exc)) from exc

    def update_chat(self, chat_id: str, patch: SuperChatPatch, user_id: str | None = None) -> SuperChatList:
        try:
            return agent_history_store.update_super_chat(user_id, chat_id, patch.model_dump(exclude_unset=True))
        except ValueError as exc:
            raise HTTPException(status_code=400, detail=str(exc)) from exc
        except KeyError as exc:
            raise HTTPException(status_code=404, detail="Chat not found") from exc

    def delete_chat(self, chat_id: str, user_id: str | None = None) -> SuperChatList:
        try:
            return agent_history_store.delete_super_chat(user_id, chat_id)
        except ValueError as exc:
            raise HTTPException(status_code=400, detail=str(exc)) from exc
        except KeyError as exc:
            raise HTTPException(status_code=404, detail="Chat not found") from exc

    def activate_chat(self, chat_id: str, user_id: str | None = None) -> SuperChatList:
        try:
            return agent_history_store.activate_super_chat(user_id, chat_id)
        except KeyError as exc:
            raise HTTPException(status_code=404, detail="Chat not found") from exc

    def read(self, user_id: str | None = None) -> SuperWorkspaceData:
        workspace, roles = agent_history_store.super_workspace_data(user_id)
        return SuperWorkspaceData(
            id=workspace.id,
            name=workspace.name,
            common_prompt=workspace.common_prompt,
            roles=[
                SuperRole(
                    id=role.id,
                    name=role.name,
                    description=role.description,
                    provider=role.provider,
                    cwd=role.cwd,
                    model=role.model,
                    session_policy=role.session_policy,
                    created_at=role.created_at,
                    updated_at=role.updated_at,
                )
                for role in roles
            ],
        )

    def write(self, data: SuperWorkspaceData, user_id: str | None = None) -> SuperWorkspaceData:
        return data

    def update(self, patch: SuperWorkspacePatch, user_id: str | None = None) -> SuperWorkspaceData:
        update = patch.model_dump(exclude_unset=True)
        if "common_prompt" in update:
            agent_history_store.update_super_workspace_common_prompt(user_id, str(update["common_prompt"] or "").strip())
        return self.read(user_id)

    def create_role(self, request: SuperRoleCreate, user_id: str | None = None) -> SuperWorkspaceData:
        try:
            agent_history_store.create_super_workspace_role(
                user_id,
                name=(request.name or "New Role").strip()[:120] or "New Role",
                description=request.description.strip(),
                provider=(request.provider or "codex").strip() or "codex",
                cwd=request.cwd.strip(),
                model=request.model.strip() if request.model else None,
                session_policy=request.session_policy,
            )
        except ValueError as exc:
            raise HTTPException(status_code=400, detail=str(exc)) from exc
        return self.read(user_id)

    def update_role(self, role_id: str, patch: SuperRolePatch, user_id: str | None = None) -> SuperWorkspaceData:
        data = self.read(user_id)
        role = self._find_role(data, role_id)
        update = patch.model_dump(exclude_unset=True)
        for key, value in update.items():
            if isinstance(value, str):
                value = value.strip()
            if key == "name":
                value = str(value or "New Role")[:120]
            if key == "provider":
                value = str(value or "codex")
            setattr(role, key, value)
        try:
            agent_history_store.update_super_workspace_role(
                user_id,
                role_id,
                {
                    "name": role.name,
                    "description": role.description,
                    "provider": role.provider,
                    "cwd": role.cwd,
                    "model": role.model,
                    "session_policy": role.session_policy,
                },
            )
        except ValueError as exc:
            raise HTTPException(status_code=400, detail=str(exc)) from exc
        return self.read(user_id)

    def delete_role(self, role_id: str, user_id: str | None = None) -> SuperWorkspaceData:
        agent_history_store.delete_super_workspace_role(user_id, role_id)
        return self.read(user_id)

    async def dispatch(self, request: SuperDispatchRequest, user_id: str | None = None) -> SuperDispatchResponse:
        message = request.message.strip()
        if not message:
            raise HTTPException(status_code=400, detail="Message is required")
        data = self.read(user_id)
        candidates = [role for role in data.roles if role.description.strip()]
        if request.role_ids:
            allowed = set(request.role_ids)
            candidates = [role for role in candidates if role.id in allowed]
        if not candidates:
            raise HTTPException(status_code=400, detail="No dispatchable roles have descriptions")
        raw = await asyncio.to_thread(self._dispatch_sync, message, candidates)
        selected = self._normalize_selected(raw, candidates)
        return SuperDispatchResponse(role_ids=selected, rationale=str(raw.get("rationale") or ""), raw=raw)

    def _find_role(self, data: SuperWorkspaceData, role_id: str) -> SuperRole:
        for role in data.roles:
            if role.id == role_id:
                return role
        raise HTTPException(status_code=404, detail="Role not found")

    def _dispatch_sync(self, message: str, roles: list[SuperRole]) -> dict[str, Any]:
        api_key = self._dispatch_api_key()
        if not api_key:
            raise HTTPException(status_code=503, detail="Set VIEWER_SUPER_DISPATCH_API_KEY or DEEPSEEK_API_KEY for LLM dispatch")
        model = os.environ.get("VIEWER_SUPER_DISPATCH_MODEL", DEFAULT_DISPATCH_MODEL)
        url = os.environ.get("VIEWER_SUPER_DISPATCH_URL", DEFAULT_DISPATCH_URL)
        payload = {
            "model": model,
            "response_format": {"type": "json_object"},
            "messages": [
                {
                    "role": "system",
                    "content": (
                        "You route one user message to the most appropriate persistent agent roles. "
                        "Return only JSON with role_ids and rationale. role_ids must be an array of ids from the provided roles. "
                        "Choose multiple roles only when the message clearly asks for multiple independent roles."
                    ),
                },
                {
                    "role": "user",
                    "content": json.dumps(
                        {
                            "message": message,
                            "roles": [
                                {
                                    "id": role.id,
                                    "name": role.name,
                                    "description": role.description,
                                    "cwd": role.cwd,
                                    "provider": role.provider,
                                }
                                for role in roles
                            ],
                        },
                        ensure_ascii=False,
                    ),
                },
            ],
        }
        request = Request(
            url,
            data=json.dumps(payload).encode("utf-8"),
            headers={"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urlopen(request, timeout=30) as response:
                response_payload = json.loads(response.read().decode("utf-8"))
        except HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")
            raise HTTPException(status_code=502, detail=f"Dispatch model failed: {detail}") from exc
        except (OSError, URLError, json.JSONDecodeError) as exc:
            raise HTTPException(status_code=502, detail=f"Dispatch model failed: {exc}") from exc
        content = response_payload.get("choices", [{}])[0].get("message", {}).get("content", "")
        try:
            parsed = json.loads(content)
        except json.JSONDecodeError as exc:
            raise HTTPException(status_code=502, detail=f"Dispatch model returned non-JSON content: {content[:500]}") from exc
        return parsed if isinstance(parsed, dict) else {}

    def _dispatch_api_key(self) -> str:
        for name in ("VIEWER_SUPER_DISPATCH_API_KEY", "VIEWER_DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY", "VIEWER_OPENAI_API_KEY", "OPENAI_API_KEY"):
            value = os.environ.get(name, "").strip()
            if value:
                return value
        for env_path in self._project_env_paths():
            value = self._read_env_value(env_path, "VIEWER_SUPER_DISPATCH_API_KEY") or self._read_env_value(env_path, "DEEPSEEK_API_KEY")
            if value:
                return value
        return ""

    def _project_env_paths(self) -> list[Path]:
        paths: list[Path] = []
        configured = os.environ.get("VIEWER_PROJECT_ENV_PATH", "").strip()
        if configured:
            paths.append(Path(configured).expanduser())
        paths.append(PROJECT_ENV_PATH)
        unique: list[Path] = []
        seen: set[str] = set()
        for path in paths:
            key = str(path)
            if key not in seen:
                unique.append(path)
                seen.add(key)
        return unique

    def _read_env_value(self, path: Path, name: str) -> str:
        try:
            lines = path.read_text(encoding="utf-8").splitlines()
        except OSError:
            return ""
        prefix = f"{name}="
        for raw in lines:
            line = raw.strip()
            if not line or line.startswith("#") or not line.startswith(prefix):
                continue
            value = line[len(prefix):].strip()
            if len(value) >= 2 and value[0] == value[-1] and value[0] in {"'", '"'}:
                value = value[1:-1]
            return value.strip()
        return ""

    def _normalize_selected(self, raw: dict[str, Any], roles: list[SuperRole]) -> list[str]:
        valid = {role.id for role in roles}
        values = raw.get("role_ids")
        if isinstance(values, str):
            values = [values]
        selected = []
        if isinstance(values, list):
            for value in values:
                role_id = str(value)
                if role_id in valid and role_id not in selected:
                    selected.append(role_id)
        if not selected:
            raise HTTPException(status_code=502, detail="Dispatch model did not select a valid role")
        return selected


super_workspace_manager = SuperWorkspaceManager()
