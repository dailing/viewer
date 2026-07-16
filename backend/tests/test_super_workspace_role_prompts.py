import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from backend.app.agent_history import AgentHistoryStore
from backend.app.super_workspace import SuperRole, SuperWorkspaceManager
from backend.app.super_workspace_runtime import CodexSuperDriver


def role() -> SuperRole:
    return SuperRole(
        id="role-1",
        name="Test Role",
        description="DISPATCH_DESCRIPTION_ONLY",
        prompt="AGENT_PROMPT_ONLY",
        provider="codex",
        cwd="project",
        created_at=1.0,
        updated_at=1.0,
    )


class RolePromptSeparationTests(unittest.TestCase):
    def test_dispatch_payload_contains_description_but_not_agent_prompt(self) -> None:
        manager = SuperWorkspaceManager()
        rendered = manager._render_dispatch_prompt(
            "route this",
            [role()],
            "",
            SimpleNamespace(dispatch_prompt_template="{{roles_json}}"),
        )
        payload = json.loads(rendered)
        self.assertEqual(payload[0]["description"], "DISPATCH_DESCRIPTION_ONLY")
        self.assertNotIn("prompt", payload[0])
        self.assertNotIn("AGENT_PROMPT_ONLY", rendered)

    def test_agent_initial_prompt_contains_role_prompt_but_not_description(self) -> None:
        workspace = SimpleNamespace(common_prompt="COMMON_PROMPT")
        with patch("backend.app.super_workspace_runtime.super_workspace_manager.read", return_value=workspace):
            rendered = CodexSuperDriver().initial_prompt(role(), "user")
        self.assertIn("COMMON_PROMPT", rendered)
        self.assertIn("AGENT_PROMPT_ONLY", rendered)
        self.assertNotIn("DISPATCH_DESCRIPTION_ONLY", rendered)

    def test_only_prompt_changes_clear_reusable_role_sessions(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            store = AgentHistoryStore(Path(directory) / "history.sqlite3")
            workspace = store.ensure_default_workspace("user")
            store.create_super_workspace_role(
                "user",
                name="Role",
                description="Dispatch description",
                prompt="Original prompt",
            )
            _, roles = store.super_workspace_data("user")
            role_id = str(roles[0].id)
            chats = store.create_super_chat(
                "user",
                name="Chat",
                member_role_ids=[role_id],
                workspace_id=workspace.id,
            )
            chat_id = chats.active_chat_id
            store.upsert_chat_role_session(
                "user",
                workspace_id=workspace.id,
                chat_id=chat_id,
                role_id=role_id,
                provider="codex",
                session_ref="codex:session-1",
                cwd="",
                model=None,
                session_policy="reuse",
            )
            store.update_super_workspace_role("user", role_id, {"description": "Updated description"})
            self.assertIsNotNone(store.get_chat_role_session("user", workspace.id, chat_id, role_id, "codex"))
            store.update_super_workspace_role("user", role_id, {"prompt": "Updated prompt"})
            self.assertIsNone(store.get_chat_role_session("user", workspace.id, chat_id, role_id, "codex"))

if __name__ == "__main__":
    unittest.main()
