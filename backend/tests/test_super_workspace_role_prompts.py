import asyncio
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch

from backend.app.agent_history import AgentHistoryStore, SuperDriverRunCreate
from backend.app.codex_background_runner import insert_provider_message
from backend.app.models import ProviderAccountConfig, RoutingCandidateConfig, RoutingPolicyConfig, SuperWorkspaceConfig
from backend.app.super_workspace import SuperRole, SuperWorkspaceManager
from backend.app.super_workspace_runtime import CodexAppServerSuperDriver, CodexSuperDriver, HermesSuperDriver, OpenCodeSuperDriver, SuperWorkspaceRuntime


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
    def test_provider_context_limits_have_configurable_defaults(self) -> None:
        config = SuperWorkspaceConfig()
        self.assertEqual(config.provider_context_limits["codex"].context_recycle_percent, 70)
        self.assertEqual(config.provider_context_limits["codex-app-server"].context_recycle_tokens, 200_000)
        self.assertIsNone(config.provider_context_limits["hermes"].context_recycle_tokens)
        self.assertIsNone(config.provider_context_limits["opencode"].context_recycle_tokens)

    def test_chat_virtual_space_is_enabled_by_default_and_can_be_disabled(self) -> None:
        self.assertTrue(SuperWorkspaceConfig().chat_virtual_space_enabled)
        self.assertFalse(SuperWorkspaceConfig(chat_virtual_space_enabled=False).chat_virtual_space_enabled)

    def test_driver_uses_provider_context_limits_from_viewer_config(self) -> None:
        limit = SimpleNamespace(context_recycle_percent=63.5, context_recycle_tokens=123_000)
        config = SimpleNamespace(super_workspace=SimpleNamespace(provider_context_limits={"codex": limit}))
        with patch("backend.app.files.read_config", return_value=config):
            driver = CodexSuperDriver()
            self.assertEqual(driver.provider_context_recycle_percent(), 63.5)
            self.assertEqual(driver.provider_context_recycle_tokens(), 123_000)

    def test_acp_failures_are_not_converted_to_success_by_visible_output(self) -> None:
        self.assertTrue(CodexSuperDriver.accept_final_response_on_failed_session)
        self.assertFalse(HermesSuperDriver.accept_final_response_on_failed_session)
        self.assertFalse(OpenCodeSuperDriver.accept_final_response_on_failed_session)

    def test_runtime_registers_opencode_acp_driver(self) -> None:
        runtime = SuperWorkspaceRuntime()
        self.assertIsInstance(runtime._drivers["opencode"], OpenCodeSuperDriver)

    def test_chat_route_override_wins_and_filters_disabled_accounts(self) -> None:
        manager = SuperWorkspaceManager()
        role_value = role().model_copy(update={"routing_policy_id": "role-policy"})
        role_policy = RoutingPolicyConfig(
            id="role-policy", name="Role", candidates=[RoutingCandidateConfig(id="role-target", runtime_id="codex")]
        )
        chat_policy = RoutingPolicyConfig(
            id="chat-policy",
            name="Chat",
            candidates=[
                RoutingCandidateConfig(id="disabled", runtime_id="hermes", provider_account_id="account-off"),
                RoutingCandidateConfig(id="enabled", runtime_id="codex-app-server", provider_account_id="account-on"),
            ],
        )
        data = SimpleNamespace(
            default_routing_policy_id="role-policy",
            routing_policies=[role_policy, chat_policy],
            provider_accounts=[
                ProviderAccountConfig(id="account-off", name="Off", provider="hermes", enabled=False),
                ProviderAccountConfig(id="account-on", name="On", provider="openai", enabled=True),
            ],
        )
        chat = SimpleNamespace(role_routing_policy_overrides={role_value.id: "chat-policy"})
        with patch.object(manager, "read", return_value=data):
            policy, candidates = manager.routing_candidates_for(role_value, chat)
        self.assertEqual(policy.id, "chat-policy")
        self.assertEqual([candidate.id for candidate in candidates], ["enabled"])

    def test_candidate_capabilities_must_satisfy_role_requirements(self) -> None:
        candidate = RoutingCandidateConfig(
            id="target",
            runtime_id="hermes",
            parameters={"capabilities": {"tools": True, "filesystem": False}, "context_window": 128_000},
        )
        self.assertTrue(SuperWorkspaceManager._candidate_meets_requirements(candidate, {"tools": True, "min_context_window": 100_000}))
        self.assertFalse(SuperWorkspaceManager._candidate_meets_requirements(candidate, {"filesystem": True}))
        self.assertFalse(SuperWorkspaceManager._candidate_meets_requirements(candidate, {"min_context_window": 200_000}))

    def test_routing_error_categories_distinguish_credit_and_request_failures(self) -> None:
        runtime = SuperWorkspaceRuntime()
        self.assertEqual(runtime._routing_error_category("insufficient credits"), "credit")
        self.assertEqual(runtime._routing_error_category("429 rate limit exceeded"), "rate_limit")
        self.assertEqual(runtime._routing_error_category("invalid request payload"), "request")

    def test_failed_session_error_is_written_to_driver_target(self) -> None:
        runtime = SuperWorkspaceRuntime()
        manager = MagicMock()
        manager.snapshot.return_value = {"status": "failed", "error": "thread/start timed out"}
        driver = CodexAppServerSuperDriver()
        driver._session_manager = manager
        failed_run = SimpleNamespace(status="failed")

        with (
            patch("backend.app.super_workspace_runtime.agent_history_store.get_dispatch_task", return_value=SimpleNamespace(status="running")),
            patch("backend.app.super_workspace_runtime.agent_history_store.update_driver_run_status") as update_status,
            patch("backend.app.super_workspace_runtime.agent_history_store.upsert_chat_role_session"),
            patch.object(runtime, "_emit_update", new=AsyncMock()),
            patch.object(runtime, "_summarize_run_status", return_value=failed_run),
        ):
            result = asyncio.run(
                runtime._wait_for_session(
                    driver,
                    "viewer-session",
                    "user",
                    "query-run",
                    "driver-run",
                    "workspace",
                    "chat",
                    role().model_copy(update={"provider": "codex-app-server"}),
                    "/tmp",
                    "codex-app-server:viewer-session",
                    None,
                )
            )

        self.assertIs(result, failed_run)
        failed_calls = [call for call in update_status.call_args_list if call.args[1] == "failed"]
        self.assertEqual(len(failed_calls), 1)
        self.assertEqual(failed_calls[0].kwargs["error"], "thread/start timed out")

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
                root="project",
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

    def test_structured_content_blocks_are_persisted_with_query(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            store = AgentHistoryStore(Path(directory) / "history.sqlite3")
            workspace = store.ensure_default_workspace("user")
            chats = store.create_super_chat("user", name="Chat", root="project", workspace_id=workspace.id)
            blocks = [
                {
                    "type": "resource",
                    "resource": {"uri": "viewer://diff", "mimeType": "text/x-diff", "text": "+changed"},
                }
            ]

            run = store.create_super_run(
                "user",
                "review this",
                "queued",
                chat_id=chats.active_chat_id,
                content_blocks=blocks,
            )

            self.assertEqual(run.content_blocks, blocks)
            self.assertEqual(run.turn_id, run.message_id)
            self.assertEqual(store.get_super_run(run.id, "user").content_blocks, blocks)

    def test_legacy_messages_are_migrated_to_one_turn_per_message(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "history.sqlite3"
            store = AgentHistoryStore(path)
            workspace = store.ensure_default_workspace("user")
            chats = store.create_super_chat("user", name="Chat", root="project", workspace_id=workspace.id)
            run = store.create_super_run("user", "legacy", "queued", chat_id=chats.active_chat_id)
            store.engine.dispose()

            with sqlite3.connect(path) as connection:
                connection.execute("DROP INDEX idx_super_messages_turn_time")
                connection.execute("ALTER TABLE super_workspace_messages DROP COLUMN turn_id")
                connection.execute(
                    "DELETE FROM agent_history_schema_migrations "
                    "WHERE id = 'super_workspace_messages_turn_id_v1'"
                )

            migrated = AgentHistoryStore(path)
            with migrated.engine.connect() as connection:
                turn_id = connection.exec_driver_sql(
                    "SELECT turn_id FROM super_workspace_messages WHERE id = ?",
                    (run.message_id,),
                ).scalar_one()
            self.assertEqual(turn_id, run.message_id)

    def test_codex_background_writer_persists_worker_turn_id(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "history.sqlite3"
            store = AgentHistoryStore(path)
            insert_provider_message(
                path,
                user_id="user",
                workspace_id=None,
                chat_id=None,
                provider="codex",
                viewer_session_id="viewer-1",
                provider_session_id=None,
                turn_id="worker-turn-1",
                query_message_id=None,
                driver_run_id=None,
                parent_message_id=None,
                sender_role_id=None,
                recipient_role_id=None,
                role_id=None,
                event_index=0,
                received_at=1.0,
                source_path=None,
                source_event_id="event-1",
                source_line=None,
                role="assistant",
                event_type="message:assistant",
                text="done",
                raw={},
            )
            with store.engine.connect() as connection:
                turn_id = connection.exec_driver_sql(
                    "SELECT turn_id FROM super_workspace_messages WHERE source_event_id = 'event-1'"
                ).scalar_one()
            self.assertEqual(turn_id, "worker-turn-1")

    def test_dispatch_task_preserves_force_new_session(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            store = AgentHistoryStore(Path(directory) / "history.sqlite3")
            workspace = store.ensure_default_workspace("user")
            chats = store.create_super_chat("user", name="Chat", root="project", workspace_id=workspace.id)
            run = store.create_super_run("user", "start fresh", "queued", chat_id=chats.active_chat_id)
            store.create_dispatch_task(
                "user",
                run.id,
                SuperDriverRunCreate(
                    workspace_id=workspace.id,
                    chat_id=chats.active_chat_id,
                    role_id="codex-app-role",
                    role_name="Codex App",
                    provider="codex-app-server",
                    force_new_session=True,
                ),
            )

            target = store.get_super_run(run.id, "user").targets[0]
            task = store.get_dispatch_task(target.id)

            self.assertIsNotNone(task)
            self.assertTrue(task.force_new_session)

if __name__ == "__main__":
    unittest.main()
