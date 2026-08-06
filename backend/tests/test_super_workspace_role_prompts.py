import asyncio
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch

from backend.app.agent_history import AgentHistoryStore, SuperDriverRunCreate
from backend.app.models import RoutingCandidateConfig, RoutingPolicyConfig, SuperWorkspaceConfig
from backend.app.super_workspace import SuperRole, SuperWorkspaceManager
from backend.app.super_workspace_runtime import CodexAppServerSuperDriver, HermesSuperDriver, OpenCodeSuperDriver, SuperWorkspaceRuntime


def candidate(candidate_id: str, agent_id: str, provider_id: str = "default", model_id: str = "model") -> RoutingCandidateConfig:
    return RoutingCandidateConfig(
        id=candidate_id,
        target_id=f"target-{candidate_id}",
        agent_id=agent_id,
        provider_id=provider_id,
        model_id=model_id,
        selection_id=model_id,
    )


def role() -> SuperRole:
    return SuperRole(
        id="role-1",
        name="Test Role",
        description="DISPATCH_DESCRIPTION_ONLY",
        prompt="AGENT_PROMPT_ONLY",
        provider="codex-app-server",
        cwd="project",
        created_at=1.0,
        updated_at=1.0,
    )


class RolePromptSeparationTests(unittest.TestCase):
    def test_provider_context_limits_have_configurable_defaults(self) -> None:
        config = SuperWorkspaceConfig()
        self.assertEqual(config.provider_context_limits["codex-app-server"].context_recycle_tokens, 200_000)
        self.assertIsNone(config.provider_context_limits["hermes"].context_recycle_tokens)
        self.assertIsNone(config.provider_context_limits["opencode"].context_recycle_tokens)

    def test_chat_virtual_space_is_enabled_by_default_and_can_be_disabled(self) -> None:
        self.assertTrue(SuperWorkspaceConfig().chat_virtual_space_enabled)
        self.assertFalse(SuperWorkspaceConfig(chat_virtual_space_enabled=False).chat_virtual_space_enabled)

    def test_agent_activity_is_hidden_by_default_and_can_be_enabled(self) -> None:
        self.assertFalse(SuperWorkspaceConfig().chat_show_agent_activity)
        self.assertTrue(SuperWorkspaceConfig(chat_show_agent_activity=True).chat_show_agent_activity)

    def test_driver_uses_provider_context_limits_from_viewer_config(self) -> None:
        limit = SimpleNamespace(context_recycle_percent=63.5, context_recycle_tokens=123_000)
        config = SimpleNamespace(super_workspace=SimpleNamespace(provider_context_limits={"codex-app-server": limit}))
        with patch("backend.app.files.read_config", return_value=config):
            driver = CodexAppServerSuperDriver()
            self.assertEqual(driver.provider_context_recycle_percent(), 63.5)
            self.assertEqual(driver.provider_context_recycle_tokens(), 123_000)

    def test_acp_failures_are_not_converted_to_success_by_visible_output(self) -> None:
        self.assertFalse(CodexAppServerSuperDriver.accept_final_response_on_failed_session)
        self.assertFalse(HermesSuperDriver.accept_final_response_on_failed_session)
        self.assertFalse(OpenCodeSuperDriver.accept_final_response_on_failed_session)

    def test_runtime_registers_opencode_acp_driver(self) -> None:
        runtime = SuperWorkspaceRuntime()
        self.assertIsInstance(runtime._drivers["opencode"], OpenCodeSuperDriver)

    def test_chat_profile_override_wins_and_filters_disabled_targets(self) -> None:
        manager = SuperWorkspaceManager()
        role_value = role().model_copy(update={"routing_policy_id": "role-policy"})
        role_policy = RoutingPolicyConfig(
            id="role-policy", name="Role", candidates=[candidate("role-target", "codex-app-server")]
        )
        chat_policy = RoutingPolicyConfig(
            id="chat-policy",
            name="Chat",
            candidates=[
                candidate("disabled", "hermes").model_copy(update={"enabled": False}),
                candidate("enabled", "codex-app-server", "openai-subscription"),
            ],
        )
        data = SimpleNamespace(
            default_routing_policy_id="role-policy",
            routing_policies=[role_policy, chat_policy],
        )
        chat = SimpleNamespace(role_routing_policy_overrides={role_value.id: "chat-policy"})
        with patch.object(manager, "read", return_value=data):
            policy, candidates = manager.routing_candidates_for(role_value, chat)
        self.assertEqual(policy.id, "chat-policy")
        self.assertEqual([candidate.id for candidate in candidates], ["enabled"])

    def test_candidate_capabilities_must_satisfy_role_requirements(self) -> None:
        target = candidate("target", "hermes").model_copy(update={
            "parameters": {"capabilities": {"tools": True, "filesystem": False}, "context_window": 128_000},
        })
        self.assertTrue(SuperWorkspaceManager._candidate_meets_requirements(target, {"tools": True, "min_context_window": 100_000}))
        self.assertFalse(SuperWorkspaceManager._candidate_meets_requirements(target, {"filesystem": True}))
        self.assertFalse(SuperWorkspaceManager._candidate_meets_requirements(target, {"min_context_window": 200_000}))

    def test_routing_error_categories_distinguish_credit_and_request_failures(self) -> None:
        driver = CodexAppServerSuperDriver()
        target = candidate("target", "codex-app-server", "openai-subscription")
        self.assertEqual(driver.normalize_error(RuntimeError("insufficient credits"), target).category, "credit")
        self.assertEqual(driver.normalize_error(RuntimeError("429 rate limit exceeded"), target).category, "rate_limit")
        self.assertEqual(driver.normalize_error(RuntimeError("invalid request payload"), target).category, "request")

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

    def test_session_waiter_only_updates_the_session_it_owns(self) -> None:
        runtime = SuperWorkspaceRuntime()
        manager = MagicMock()
        manager.snapshot.return_value = {"status": "completed"}
        driver = CodexAppServerSuperDriver()
        driver._session_manager = manager
        completed_run = SimpleNamespace(status="completed")

        with (
            patch("backend.app.super_workspace_runtime.agent_history_store.get_dispatch_task", return_value=SimpleNamespace(status="running")),
            patch("backend.app.super_workspace_runtime.agent_history_store.update_driver_run_status"),
            patch("backend.app.super_workspace_runtime.agent_history_store.upsert_chat_role_session") as upsert_session,
            patch.object(runtime, "_emit_update", new=AsyncMock()),
            patch.object(runtime, "_summarize_run_status", return_value=completed_run),
        ):
            result = asyncio.run(
                runtime._wait_for_session(
                    driver,
                    "parallel-session",
                    "user",
                    "query-run",
                    "driver-run",
                    "workspace",
                    "chat",
                    role().model_copy(update={"provider": "codex-app-server"}),
                    "/tmp",
                    "codex-app-server:parallel-session",
                    None,
                )
            )

        self.assertIs(result, completed_run)
        upsert_session.assert_called_once()
        self.assertFalse(upsert_session.call_args.kwargs["replace_session"])

    def test_stale_session_usage_cannot_replace_new_primary_session(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            store = AgentHistoryStore(Path(directory) / "history.sqlite3")
            workspace = store.ensure_default_workspace("user")
            chats = store.create_super_chat("user", name="Chat", root="project", workspace_id=workspace.id)
            common = {
                "workspace_id": workspace.id,
                "chat_id": chats.active_chat_id,
                "role_id": "same-role",
                "provider": "codex-app-server",
                "cwd": "/tmp",
                "model": None,
                "session_policy": "reuse",
            }
            store.upsert_chat_role_session("user", session_ref="codex-app-server:old", driver_run_id="old-run", **common)
            store.upsert_chat_role_session("user", session_ref="codex-app-server:lightning", driver_run_id="lightning-run", **common)

            state = store.upsert_chat_role_session(
                "user",
                session_ref="codex-app-server:old",
                driver_run_id="old-run",
                total_tokens=123,
                replace_session=False,
                **common,
            )

            self.assertEqual(state.session_ref, "codex-app-server:lightning")
            self.assertEqual(state.last_driver_run_id, "lightning-run")
            self.assertIsNone(state.total_tokens)

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
            rendered = CodexAppServerSuperDriver().initial_prompt(role(), "user")
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
                provider="codex-app-server",
                session_ref="codex-app-server:session-1",
                cwd="",
                model=None,
                session_policy="reuse",
            )
            store.update_super_workspace_role("user", role_id, {"description": "Updated description"})
            self.assertIsNotNone(store.get_chat_role_session("user", workspace.id, chat_id, role_id, "codex-app-server"))
            store.update_super_workspace_role("user", role_id, {"prompt": "Updated prompt"})
            self.assertIsNone(store.get_chat_role_session("user", workspace.id, chat_id, role_id, "codex-app-server"))

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

    def test_legacy_codex_roles_are_migrated_and_old_sessions_removed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "history.sqlite3"
            store = AgentHistoryStore(path)
            workspace = store.ensure_default_workspace("user")
            store.create_super_workspace_role("user", name="Legacy", provider="codex")
            _, roles = store.super_workspace_data("user")
            role_id = str(roles[0].id)
            chats = store.create_super_chat("user", name="Chat", root="project", workspace_id=workspace.id)
            store.upsert_chat_role_session(
                "user", workspace_id=workspace.id, chat_id=chats.active_chat_id,
                role_id=role_id, provider="codex", session_ref="codex:old", cwd="", model=None,
                session_policy="reuse",
            )
            store.engine.dispose()
            with sqlite3.connect(path) as connection:
                connection.execute(
                    "DELETE FROM agent_history_schema_migrations WHERE id = 'remove_legacy_codex_driver_v1'"
                )

            migrated = AgentHistoryStore(path)
            _, migrated_roles = migrated.super_workspace_data("user")

            self.assertEqual(migrated_roles[0].provider, "codex-app-server")
            self.assertIsNone(
                migrated.get_chat_role_session("user", workspace.id, chats.active_chat_id, role_id, "codex")
            )

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

    def test_parallel_new_session_can_be_claimed_while_same_role_is_running(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            store = AgentHistoryStore(Path(directory) / "history.sqlite3")
            workspace = store.ensure_default_workspace("user")
            chats = store.create_super_chat("user", name="Chat", root="project", workspace_id=workspace.id)

            def create_task(message: str, *, parallel: bool = False):
                run = store.create_super_run("user", message, "queued", chat_id=chats.active_chat_id)
                store.create_dispatch_task(
                    "user",
                    run.id,
                    SuperDriverRunCreate(
                        workspace_id=workspace.id,
                        chat_id=chats.active_chat_id,
                        role_id="same-role",
                        role_name="Same Role",
                        provider="codex-app-server",
                        force_new_session=parallel,
                        parallel_dispatch=parallel,
                    ),
                )
                return run

            create_task("first")
            first = store.claim_next_dispatch_task("worker")
            self.assertIsNotNone(first)
            store.update_driver_run_status(first.id, "running")
            create_task("serial")
            parallel_run = create_task("parallel", parallel=True)

            claimed = store.claim_next_dispatch_task("worker")

            self.assertIsNotNone(claimed)
            self.assertEqual(claimed.query_message_id, parallel_run.id)
            self.assertTrue(claimed.force_new_session)
            self.assertTrue(claimed.parallel_dispatch)

    def test_orphan_sweep_preserves_run_owned_by_live_draining_worker(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            store = AgentHistoryStore(Path(directory) / "history.sqlite3")
            workspace = store.ensure_default_workspace("user")
            chats = store.create_super_chat("user", name="Chat", root="project", workspace_id=workspace.id)
            run = store.create_super_run("user", "keep draining", "queued", chat_id=chats.active_chat_id)
            store.create_dispatch_task(
                "user",
                run.id,
                SuperDriverRunCreate(
                    workspace_id=workspace.id,
                    chat_id=chats.active_chat_id,
                    role_id="hermes-role",
                    role_name="Hermes",
                    provider="hermes",
                ),
            )
            task = store.claim_next_dispatch_task("backend:old-worker")
            self.assertIsNotNone(task)
            store.update_driver_run_status(task.id, "running")

            with patch("backend.app.agent_history.process_slot_state", return_value={"alive": True}):
                interrupted = store.interrupt_orphaned_running_driver_runs()

            self.assertEqual(interrupted, 0)
            self.assertEqual(store.get_dispatch_task(task.id).status, "running")

    def test_orphan_sweep_interrupts_run_after_owning_worker_dies(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            store = AgentHistoryStore(Path(directory) / "history.sqlite3")
            workspace = store.ensure_default_workspace("user")
            chats = store.create_super_chat("user", name="Chat", root="project", workspace_id=workspace.id)
            run = store.create_super_run("user", "orphan me", "queued", chat_id=chats.active_chat_id)
            store.create_dispatch_task(
                "user",
                run.id,
                SuperDriverRunCreate(
                    workspace_id=workspace.id,
                    chat_id=chats.active_chat_id,
                    role_id="hermes-role",
                    role_name="Hermes",
                    provider="hermes",
                ),
            )
            task = store.claim_next_dispatch_task("backend:dead-worker")
            self.assertIsNotNone(task)
            store.update_driver_run_status(task.id, "running")

            with patch("backend.app.agent_history.process_slot_state", return_value={"alive": False}):
                interrupted = store.interrupt_orphaned_running_driver_runs()

            self.assertEqual(interrupted, 1)
            self.assertEqual(store.get_dispatch_task(task.id).status, "interrupted")

    def test_provider_health_applies_to_every_model_in_provider(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            store = AgentHistoryStore(Path(directory) / "history.sqlite3")
            workspace = store.ensure_default_workspace("user")
            store.record_target_failure(
                "user", workspace.id,
                scope_type="provider", scope_id="hermes/deepseek",
                error_category="credit", last_error="credits exhausted", retry_after=9_999_999_999,
            )
            task = SimpleNamespace(user_id="user", workspace_id=workspace.id)
            first = candidate("chat", "hermes", "deepseek", "deepseek-chat")
            second = candidate("reasoner", "hermes", "deepseek", "deepseek-reasoner")
            with patch("backend.app.super_workspace_runtime.agent_history_store", store):
                self.assertIsNotNone(SuperWorkspaceRuntime._blocked_health(task, first))
                self.assertIsNotNone(SuperWorkspaceRuntime._blocked_health(task, second))

if __name__ == "__main__":
    unittest.main()
