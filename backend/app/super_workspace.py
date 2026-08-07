import asyncio
import hashlib
import json
import re
from typing import Any

from fastapi import HTTPException
from pydantic import BaseModel, Field

from .agent_history import DEFAULT_SUPER_WORKSPACE_ID, DEFAULT_SUPER_WORKSPACE_NAME, SuperChatList, SuperChatSummary, agent_history_store
from .files import read_config, write_config
from .llm_client import LLMChainError, chat_completion
from .models import (
    DEFAULT_DISPATCH_PROMPT_TEMPLATE,
    RoutingCandidateConfig,
    RoutingPolicyConfig,
)
from .inference import inference_target_id


DEFAULT_DISPATCH_WORD_BUDGET = 2048
DISPATCH_TEMPLATE_PATTERN = re.compile(r"\{\{(?:message|history|roles_json|roles_table)\}\}")


class SuperRole(BaseModel):
    id: str
    name: str
    description: str = ""
    prompt: str = ""
    provider: str = "codex-app-server"
    cwd: str = ""
    model: str | None = None
    routing_policy_id: str = ""
    capability_requirements: dict[str, Any] = Field(default_factory=dict)
    session_policy: str = "reuse"
    context_recycle_percent: float | None = None
    context_recycle_tokens: int | None = None
    created_at: float
    updated_at: float


class SuperRoleCreate(BaseModel):
    name: str
    description: str = ""
    prompt: str = ""
    provider: str = "codex-app-server"
    cwd: str = ""
    model: str | None = None
    routing_policy_id: str = ""
    capability_requirements: dict[str, Any] = Field(default_factory=dict)
    session_policy: str = "reuse"
    context_recycle_percent: float | None = None
    context_recycle_tokens: int | None = None


class SuperRolePatch(BaseModel):
    name: str | None = None
    description: str | None = None
    prompt: str | None = None
    provider: str | None = None
    cwd: str | None = None
    model: str | None = None
    routing_policy_id: str | None = None
    capability_requirements: dict[str, Any] | None = None
    session_policy: str | None = None
    context_recycle_percent: float | None = None
    context_recycle_tokens: int | None = None


class SuperWorkspaceData(BaseModel):
    id: str = DEFAULT_SUPER_WORKSPACE_ID
    name: str = DEFAULT_SUPER_WORKSPACE_NAME
    common_prompt: str = ""
    roles: list[SuperRole] = Field(default_factory=list)
    routing_policies: list[RoutingPolicyConfig] = Field(default_factory=list)
    default_routing_policy_id: str = ""


class SuperWorkspacePatch(BaseModel):
    common_prompt: str | None = None


class RoutingConfigData(BaseModel):
    default_routing_policy_id: str = ""
    routing_policies: list[RoutingPolicyConfig] = Field(default_factory=list)


class SuperChatCreate(BaseModel):
    name: str = "New Chat"
    type: str = "group"
    pinned: bool = False
    root: str
    common_prompt: str = ""
    member_role_ids: list[str] = Field(default_factory=list)
    role_routing_policy_overrides: dict[str, str] = Field(default_factory=dict)


class SuperChatPatch(BaseModel):
    name: str | None = None
    type: str | None = None
    pinned: bool | None = None
    root: str | None = None
    common_prompt: str | None = None
    member_role_ids: list[str] | None = None
    role_routing_policy_overrides: dict[str, str] | None = None


class SuperDispatchRequest(BaseModel):
    message: str
    role_ids: list[str] | None = None
    chat_id: str | None = None
    before_message_id: str | None = None
    history_word_budget: int | None = None


class SuperDispatchResponse(BaseModel):
    role_ids: list[str]
    rationale: str = ""
    raw: dict[str, Any] | None = None


class SuperWorkspaceManager:
    def read_routing(self, user_id: str | None = None) -> RoutingConfigData:
        routing = self._ensure_role_routing_migration(user_id)
        return RoutingConfigData(
            default_routing_policy_id=routing.default_routing_policy_id,
            routing_policies=routing.routing_policies,
        )

    def update_routing(self, request: RoutingConfigData, user_id: str | None = None) -> RoutingConfigData:
        policy_ids = [policy.id.strip() for policy in request.routing_policies]
        if any(not policy_id for policy_id in policy_ids) or len(policy_ids) != len(set(policy_ids)):
            raise HTTPException(status_code=400, detail="Routing policy IDs must be non-empty and unique")
        policy_id_set = set(policy_ids)
        if policy_ids and request.default_routing_policy_id not in policy_id_set:
            raise HTTPException(status_code=400, detail="Workspace default must reference an existing routing policy")
        for policy in request.routing_policies:
            candidate_ids = [candidate.id.strip() for candidate in policy.candidates]
            if any(not candidate_id for candidate_id in candidate_ids) or len(candidate_ids) != len(set(candidate_ids)):
                raise HTTPException(status_code=400, detail=f"Candidate IDs must be unique in policy: {policy.name}")
            if any(not candidate.agent_id.strip() or not candidate.target_id.strip() for candidate in policy.candidates):
                raise HTTPException(status_code=400, detail=f"Every candidate must select an inference target: {policy.name}")
        _workspace, roles = agent_history_store.super_workspace_data(user_id)
        orphaned_roles = [str(role.name) for role in roles if role.routing_policy_id and role.routing_policy_id not in policy_id_set]
        if orphaned_roles:
            raise HTTPException(status_code=400, detail=f"Reassign Roles before deleting their routing policy: {orphaned_roles[0]}")
        chats = agent_history_store.list_super_chats(user_id).chats
        orphaned_chats = [
            chat.name
            for chat in chats
            if any(policy_id not in policy_id_set for policy_id in chat.role_routing_policy_overrides.values())
        ]
        if orphaned_chats:
            raise HTTPException(status_code=400, detail=f"Remove Chat overrides before deleting their routing policy: {orphaned_chats[0]}")
        config = read_config()
        config.super_workspace.default_routing_policy_id = request.default_routing_policy_id
        config.super_workspace.routing_policies = request.routing_policies
        write_config(config)
        return self.read_routing(user_id)

    def list_chats(self, user_id: str | None = None) -> SuperChatList:
        return agent_history_store.list_super_chats(user_id)

    def active_chat(self, user_id: str | None = None, chat_id: str | None = None) -> SuperChatSummary:
        try:
            if chat_id:
                workspace = agent_history_store.super_workspace(user_id)
                chats = agent_history_store.list_super_chats(user_id, workspace.id)
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
        self._validate_chat_routing_overrides(request.role_routing_policy_overrides, user_id)
        try:
            return agent_history_store.create_super_chat(
                user_id,
                name=request.name,
                chat_type=request.type,
                pinned=request.pinned,
                common_prompt=request.common_prompt,
                member_role_ids=request.member_role_ids,
                role_routing_policy_overrides=request.role_routing_policy_overrides,
                root=request.root,
            )
        except ValueError as exc:
            raise HTTPException(status_code=400, detail=str(exc)) from exc

    def update_chat(self, chat_id: str, patch: SuperChatPatch, user_id: str | None = None) -> SuperChatList:
        if patch.role_routing_policy_overrides is not None:
            self._validate_chat_routing_overrides(patch.role_routing_policy_overrides, user_id)
        try:
            return agent_history_store.update_super_chat(user_id, chat_id, patch.model_dump(exclude_unset=True))
        except ValueError as exc:
            raise HTTPException(status_code=400, detail=str(exc)) from exc
        except KeyError as exc:
            raise HTTPException(status_code=404, detail="Chat not found") from exc

    def _validate_chat_routing_overrides(self, overrides: dict[str, str], user_id: str | None) -> None:
        if not overrides:
            return
        routing = self.read_routing(user_id)
        policy_ids = {policy.id for policy in routing.routing_policies}
        unknown = next((policy_id for policy_id in overrides.values() if policy_id not in policy_ids), "")
        if unknown:
            raise HTTPException(status_code=400, detail=f"Chat routing policy does not exist: {unknown}")

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
        routing = self._ensure_role_routing_migration(user_id)
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
                    prompt=role.prompt,
                    provider=role.provider,
                    cwd=role.cwd,
                    model=role.model,
                    routing_policy_id=str(role.routing_policy_id or ""),
                    capability_requirements=agent_history_store._parse_json(role.capability_requirements_json, {}),
                    session_policy=role.session_policy,
                    context_recycle_percent=role.context_recycle_percent,
                    context_recycle_tokens=role.context_recycle_tokens,
                    created_at=role.created_at,
                    updated_at=role.updated_at,
                )
                for role in roles
            ],
            routing_policies=routing.routing_policies,
            default_routing_policy_id=routing.default_routing_policy_id,
        )

    def _ensure_role_routing_migration(self, user_id: str | None) -> Any:
        config = read_config()
        routing = config.super_workspace
        _workspace, roles = agent_history_store.super_workspace_data(user_id)
        policies = {policy.id: policy for policy in routing.routing_policies}
        config_changed = False
        for role in roles:
            if str(role.routing_policy_id or "").strip():
                continue
            runtime_id = str(role.provider or "codex-app-server")
            if runtime_id == "codex":
                runtime_id = "codex-app-server"
            model_id = str(role.model or "").strip() or None
            signature = f"{runtime_id}\0{model_id or ''}"
            suffix = hashlib.sha256(signature.encode("utf-8")).hexdigest()[:10]
            policy_id = f"migrated-{suffix}"
            if policy_id not in policies:
                label = f"{runtime_id} / {model_id or 'default'}"
                policy = RoutingPolicyConfig(
                    id=policy_id,
                    name=label,
                    description="Migrated from the former Role provider/model binding.",
                    candidates=[
                        RoutingCandidateConfig(
                            id=f"{policy_id}-primary",
                            name=label,
                            target_id=inference_target_id(
                                runtime_id,
                                "openai-subscription" if runtime_id == "codex-app-server" else "default",
                                model_id or "",
                            ),
                            agent_id=runtime_id,
                            provider_id="openai-subscription" if runtime_id == "codex-app-server" else "default",
                            model_id=model_id or "",
                            selection_id=model_id or "",
                        )
                    ],
                )
                routing.routing_policies.append(policy)
                policies[policy_id] = policy
                config_changed = True
            agent_history_store.update_super_workspace_role(user_id, str(role.id), {"routing_policy_id": policy_id})
        if not routing.default_routing_policy_id and routing.routing_policies:
            routing.default_routing_policy_id = routing.routing_policies[0].id
            config_changed = True
        if config_changed:
            write_config(config)
        return routing

    def routing_policy_for(self, role: SuperRole, chat: SuperChatSummary) -> RoutingPolicyConfig:
        data = self.read(None)
        policy_id = chat.role_routing_policy_overrides.get(role.id) or role.routing_policy_id or data.default_routing_policy_id
        policy = next((item for item in data.routing_policies if item.id == policy_id and item.enabled), None)
        if policy is None:
            raise HTTPException(status_code=400, detail=f"No enabled routing policy for role: {role.name}")
        return policy

    def routing_candidates_for(self, role: SuperRole, chat: SuperChatSummary) -> tuple[RoutingPolicyConfig, list[RoutingCandidateConfig]]:
        data = self.read(None)
        policy_id = chat.role_routing_policy_overrides.get(role.id) or role.routing_policy_id or data.default_routing_policy_id
        policy = next((item for item in data.routing_policies if item.id == policy_id and item.enabled), None)
        if policy is None:
            raise HTTPException(status_code=400, detail=f"No enabled routing policy for role: {role.name}")
        candidates = [
            candidate
            for candidate in policy.candidates
            if candidate.enabled
            and self._candidate_meets_requirements(candidate, role.capability_requirements)
        ]
        if not candidates:
            raise HTTPException(status_code=400, detail=f"Routing policy has no eligible candidates for role: {role.name}")
        return policy, candidates[: policy.max_attempts]

    @staticmethod
    def _candidate_meets_requirements(candidate: RoutingCandidateConfig, requirements: dict[str, Any]) -> bool:
        if not requirements:
            return True
        declared = candidate.parameters.get("capabilities", {})
        if isinstance(declared, list):
            declared = {str(value): True for value in declared}
        if not isinstance(declared, dict):
            declared = {}
        for capability in ("tools", "filesystem"):
            if requirements.get(capability) and not declared.get(capability):
                return False
        minimum_context = requirements.get("min_context_window")
        if isinstance(minimum_context, (int, float)) and minimum_context > 0:
            context_window = candidate.parameters.get("context_window")
            if not isinstance(context_window, (int, float)) or context_window < minimum_context:
                return False
        return True

    def update(self, patch: SuperWorkspacePatch, user_id: str | None = None) -> SuperWorkspaceData:
        update = patch.model_dump(exclude_unset=True)
        if "common_prompt" in update:
            agent_history_store.update_super_workspace_common_prompt(user_id, str(update["common_prompt"] or "").strip())
        return self.read(user_id)

    def create_role(self, request: SuperRoleCreate, user_id: str | None = None) -> SuperWorkspaceData:
        routing = self.read_routing(user_id)
        routing_policy_id = request.routing_policy_id.strip() or routing.default_routing_policy_id
        if routing_policy_id and routing_policy_id not in {policy.id for policy in routing.routing_policies}:
            raise HTTPException(status_code=400, detail="Role routing policy does not exist")
        try:
            agent_history_store.create_super_workspace_role(
                user_id,
                name=(request.name or "New Role").strip()[:120] or "New Role",
                description=request.description.strip(),
                prompt=request.prompt.strip(),
                provider=(request.provider or "codex-app-server").strip() or "codex-app-server",
                cwd=request.cwd.strip(),
                model=request.model.strip() if request.model else None,
                routing_policy_id=routing_policy_id,
                capability_requirements=request.capability_requirements,
                session_policy=request.session_policy,
                context_recycle_percent=request.context_recycle_percent,
                context_recycle_tokens=request.context_recycle_tokens,
            )
        except ValueError as exc:
            raise HTTPException(status_code=400, detail=str(exc)) from exc
        return self.read(user_id)

    def update_role(self, role_id: str, patch: SuperRolePatch, user_id: str | None = None) -> SuperWorkspaceData:
        data = self.read(user_id)
        role = self._find_role(data, role_id)
        update = patch.model_dump(exclude_unset=True)
        if "routing_policy_id" in update:
            requested_policy_id = str(update.get("routing_policy_id") or "").strip() or data.default_routing_policy_id
            if requested_policy_id not in {policy.id for policy in data.routing_policies}:
                raise HTTPException(status_code=400, detail="Role routing policy does not exist")
            update["routing_policy_id"] = requested_policy_id
        for key, value in update.items():
            if isinstance(value, str):
                value = value.strip()
            if key == "model" and isinstance(value, str):
                value = value or None
            if key == "name":
                value = str(value or "New Role")[:120]
            if key == "provider":
                value = str(value or "codex-app-server")
            setattr(role, key, value)
        try:
            agent_history_store.update_super_workspace_role(
                user_id,
                role_id,
                {
                    "name": role.name,
                    "description": role.description,
                    "prompt": role.prompt,
                    "provider": role.provider,
                    "cwd": role.cwd,
                    "model": role.model,
                    "routing_policy_id": role.routing_policy_id,
                    "capability_requirements": role.capability_requirements,
                    "session_policy": role.session_policy,
                    "context_recycle_percent": role.context_recycle_percent,
                    "context_recycle_tokens": role.context_recycle_tokens,
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
        history_context = ""
        if request.chat_id:
            history_budget = request.history_word_budget
            if history_budget is None:
                history_budget = int(getattr(self._llm_config(), "dispatch_history_word_budget", DEFAULT_DISPATCH_WORD_BUDGET) or DEFAULT_DISPATCH_WORD_BUDGET)
            history_context = agent_history_store.visible_chat_history_context(
                user_id or "",
                data.id,
                request.chat_id,
                request.before_message_id,
                max(0, min(int(history_budget or 0), 8000)),
            )
        raw = await asyncio.to_thread(self._dispatch_sync, message, candidates, history_context)
        selected = self._normalize_selected(raw, candidates)
        return SuperDispatchResponse(role_ids=selected, rationale=str(raw.get("rationale") or ""), raw=raw)

    def _find_role(self, data: SuperWorkspaceData, role_id: str) -> SuperRole:
        for role in data.roles:
            if role.id == role_id:
                return role
        raise HTTPException(status_code=404, detail="Role not found")

    def _dispatch_sync(self, message: str, roles: list[SuperRole], history_context: str = "") -> dict[str, Any]:
        prompt = self._render_dispatch_prompt(message, roles, history_context, self._llm_config())
        messages = [
            {
                "role": "system",
                "content": (
                    "Follow the routing prompt exactly. Return only a JSON object with role_ids and rationale. "
                    "role_ids must be an array of ids from the provided roles."
                ),
            },
            {
                "role": "user",
                "content": prompt,
            },
        ]
        try:
            result = chat_completion(messages, response_format={"type": "json_object"}, timeout=30)
        except LLMChainError as exc:
            raise HTTPException(status_code=502, detail=f"Dispatch model failed: {exc}") from exc
        content = result.content
        try:
            parsed = json.loads(content)
        except json.JSONDecodeError as exc:
            raise HTTPException(status_code=502, detail=f"Dispatch model returned non-JSON content: {content[:500]}") from exc
        return parsed if isinstance(parsed, dict) else {}

    @staticmethod
    def _llm_config() -> Any | None:
        try:
            return read_config().super_workspace
        except Exception:
            return None

    def _render_dispatch_prompt(self, message: str, roles: list[SuperRole], history_context: str, config: Any | None) -> str:
        template = str(getattr(config, "dispatch_prompt_template", "") or "").strip() or DEFAULT_DISPATCH_PROMPT_TEMPLATE
        roles_payload = [self._dispatch_role_payload(role) for role in roles]
        roles_json = json.dumps(roles_payload, ensure_ascii=False, indent=2)
        roles_table = "\n".join(
            f"- {role['id']} | {role['name']} | provider={role['provider']} | cwd={role['cwd'] or '-'}\n  {role['description']}"
            for role in roles_payload
        )
        replacements = {
            "{{message}}": message,
            "{{history}}": history_context,
            "{{roles_json}}": roles_json,
            "{{roles_table}}": roles_table,
        }
        return DISPATCH_TEMPLATE_PATTERN.sub(lambda match: replacements[match.group(0)], template)

    @staticmethod
    def _dispatch_role_payload(role: SuperRole) -> dict[str, str]:
        return {
            "id": role.id,
            "name": role.name,
            "description": role.description,
            "cwd": role.cwd,
            "provider": role.provider,
        }

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
