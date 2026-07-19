from __future__ import annotations

import asyncio
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, Mock, patch

from acp.schema import AgentCapabilities, AgentMessageChunk, PromptCapabilities, TextContentBlock, ToolCallProgress, ToolCallStart

from app.acp_runtime import ACPProcessConfig, ACPRuntime
from app.acp_sessions import ACPSession, ACPSessionManager
from app.agent_history import SuperChatRoleSessionState
from app.hermes_acp import HermesACPRuntime, HermesACPSessionNotFound
from app.hermes_sessions import HermesSessionManager
from app.super_workspace import SuperRole
from app.super_workspace_runtime import HermesSuperDriver


def manager_session(viewer_id: str, provider_id: str):
    from app.hermes_sessions import HermesSession

    return HermesSession(
        provider="hermes",
        id=viewer_id,
        user_id="dailing",
        title="test",
        cwd="/srv/projects/demo",
        model=None,
        created_at=1,
        updated_at=1,
        provider_session_id=provider_id,
        status="running",
        acp_turn_index=1,
    )


class HermesACPSessionTests(unittest.IsolatedAsyncioTestCase):
    def manager(self) -> HermesSessionManager:
        manager = HermesSessionManager()
        manager._loaded = True
        manager._cwd_for = Mock(return_value="/srv/projects/demo")
        manager._write_meta = Mock()
        manager.acp = SimpleNamespace(
            profile="default",
            yolo=True,
            new_session=AsyncMock(return_value="acp-session-1"),
            ensure_session=AsyncMock(),
            prompt=AsyncMock(return_value=SimpleNamespace(stop_reason="end_turn")),
            cancel=AsyncMock(),
            shutdown=AsyncMock(),
            capabilities_snapshot=Mock(return_value={}),
        )
        return manager

    async def wait_for_run(self, manager: HermesSessionManager, session_id: str) -> None:
        task = manager.sessions[session_id].run_task
        self.assertIsNotNone(task)
        await asyncio.wait_for(task, timeout=1)

    async def test_new_session_uses_resolved_chat_root_as_acp_cwd(self) -> None:
        manager = self.manager()
        created = await manager.create("hello", "demo", None, "dailing")
        await self.wait_for_run(manager, created["id"])

        manager.acp.new_session.assert_awaited_once_with("/srv/projects/demo", None)
        manager.acp.prompt.assert_awaited_once_with("acp-session-1", "hello")
        snapshot = manager.snapshot(created["id"])
        self.assertEqual(snapshot["provider_session_id"], "acp-session-1")
        self.assertEqual(snapshot["status"], "exited")
        self.assertEqual(snapshot["transport"], "acp")

    async def test_followup_reuses_and_reloads_same_acp_session(self) -> None:
        manager = self.manager()
        created = await manager.create("first", "demo", None, "dailing")
        await self.wait_for_run(manager, created["id"])
        manager.acp.prompt.reset_mock()

        await manager.send(created["id"], "second")
        await self.wait_for_run(manager, created["id"])

        manager.acp.ensure_session.assert_awaited_once_with("acp-session-1", "/srv/projects/demo", None)
        manager.acp.prompt.assert_awaited_once_with("acp-session-1", "second")

    async def test_followup_ignores_provider_history_replay_during_session_load(self) -> None:
        manager = self.manager()
        created = await manager.create("first", "demo", None, "dailing")
        await self.wait_for_run(manager, created["id"])
        session = manager.sessions[created["id"]]
        manager._record_lineage_events = Mock()

        async def replay_history(*_args, **_kwargs) -> None:
            await manager._handle_acp_update(
                "acp-session-1",
                AgentMessageChunk(
                    sessionUpdate="agent_message_chunk",
                    content=TextContentBlock(type="text", text="old response"),
                ),
            )

        manager.acp.ensure_session.side_effect = replay_history
        await manager.send(created["id"], "second")
        await self.wait_for_run(manager, created["id"])

        self.assertFalse(session.loading_provider_history)
        self.assertEqual(session.events, [])
        self.assertEqual(session.acp_events, [])
        self.assertEqual(session.acp_event_keys, {})
        manager._record_lineage_events.assert_not_called()

    async def test_missing_legacy_session_is_rejected_before_turn_is_accepted(self) -> None:
        manager = self.manager()
        created = await manager.create("first", "demo", None, "dailing")
        await self.wait_for_run(manager, created["id"])
        manager.acp.ensure_session.side_effect = HermesACPSessionNotFound(
            "Hermes ACP session not found: acp-session-1"
        )

        with self.assertRaisesRegex(HermesACPSessionNotFound, "ACP session not found"):
            await manager.send(created["id"], "second")

        snapshot = manager.snapshot(created["id"])
        self.assertEqual(snapshot["status"], "exited")
        self.assertEqual(len(snapshot["prompts"]), 1)
        manager.acp.prompt.assert_awaited_once_with("acp-session-1", "first")

    async def test_acp_error_is_exposed_in_snapshot(self) -> None:
        manager = self.manager()
        manager.acp.prompt.side_effect = RuntimeError("ACP transport closed")

        with patch("app.acp_sessions.logger.exception"):
            created = await manager.create("hello", "demo", None, "dailing")
            await self.wait_for_run(manager, created["id"])

        snapshot = manager.snapshot(created["id"])
        self.assertEqual(snapshot["status"], "failed")
        self.assertEqual(snapshot["error"], "ACP transport closed")

    async def test_structured_prompt_is_forwarded_as_acp_content_blocks(self) -> None:
        manager = self.manager()
        blocks = [
            {"type": "text", "text": "inspect this"},
            {
                "type": "resource",
                "resource": {"uri": "viewer://diff", "mimeType": "text/x-diff", "text": "+changed"},
            },
        ]

        created = await manager.create(blocks, "demo", None, "dailing")
        await self.wait_for_run(manager, created["id"])

        manager.acp.prompt.assert_awaited_once_with("acp-session-1", blocks)
        self.assertEqual(manager.sessions[created["id"]].prompts[0]["content_blocks"], blocks)

    async def test_acp_message_chunks_and_tool_updates_are_coalesced(self) -> None:
        manager = self.manager()
        session = manager.sessions.setdefault(
            "viewer-1",
            manager_session("viewer-1", "acp-session-1"),
        )
        manager._record_lineage_events = Mock()

        await manager._handle_acp_update(
            "acp-session-1",
            AgentMessageChunk(
                sessionUpdate="agent_message_chunk",
                content=TextContentBlock(type="text", text="Hello "),
            ),
        )
        await manager._handle_acp_update(
            "acp-session-1",
            AgentMessageChunk(
                sessionUpdate="agent_message_chunk",
                content=TextContentBlock(type="text", text="world"),
            ),
        )
        await manager._handle_acp_update(
            "acp-session-1",
            ToolCallStart(sessionUpdate="tool_call", toolCallId="tool-1", title="Run tests", status="in_progress"),
        )
        await manager._handle_acp_update(
            "acp-session-1",
            ToolCallProgress(sessionUpdate="tool_call_update", toolCallId="tool-1", status="completed"),
        )

        self.assertEqual(len(session.acp_events), 2)
        self.assertEqual(session.acp_events[0]["text"], "Hello world")
        self.assertIn("completed", session.acp_events[1]["text"])

    async def test_acp_message_chunks_preserve_markdown_whitespace(self) -> None:
        manager = self.manager()
        session = manager.sessions.setdefault(
            "viewer-1",
            manager_session("viewer-1", "acp-session-1"),
        )
        manager._record_lineage_events = Mock()
        chunks = [
            "##",
            " ",
            "Title",
            "\n\n",
            "| Column | Value |",
            "\n",
            "|:---|:---|",
            "\n",
            "| A | B |",
        ]

        for chunk in chunks:
            await manager._handle_acp_update(
                "acp-session-1",
                AgentMessageChunk(
                    sessionUpdate="agent_message_chunk",
                    content=TextContentBlock(type="text", text=chunk),
                ),
            )

        self.assertEqual(
            session.acp_events[0]["text"],
            "## Title\n\n| Column | Value |\n|:---|:---|\n| A | B |",
        )

    async def test_missing_session_rotates_during_same_super_workspace_dispatch(self) -> None:
        manager = SimpleNamespace(
            snapshot=Mock(
                return_value={
                    "status": "exited",
                    "cwd": "/srv/projects/demo",
                    "model": "k3",
                    "context_used_percent": 10,
                }
            ),
            send=AsyncMock(side_effect=HermesACPSessionNotFound("legacy HTTP session")),
            create=AsyncMock(return_value={"id": "viewer-acp-session-2"}),
        )
        driver = HermesSuperDriver()
        driver.manager = Mock(return_value=manager)
        driver.initial_prompt = Mock(return_value="ROLE PROMPT")
        driver.chat_history_prompt = Mock(return_value="")
        role = SuperRole(
            id="hermes-role",
            name="Hermes",
            provider="hermes",
            model="k3",
            created_at=1,
            updated_at=1,
        )
        state = SuperChatRoleSessionState(
            id="mapping-1",
            workspace_id="workspace-1",
            chat_id="chat-1",
            user_id="dailing",
            role_id=role.id,
            provider="hermes",
            session_ref="hermes:viewer-http-session-1",
            viewer_session_id="viewer-http-session-1",
            cwd="/srv/projects/demo",
            model="k3",
            created_at=1,
            updated_at=1,
        )

        result = await driver.dispatch_task(
            role,
            state,
            "dailing",
            "hello",
            {"chat_id": "chat-1"},
            "/srv/projects/demo",
        )

        self.assertEqual(result["session_ref"], "hermes:viewer-acp-session-2")
        self.assertEqual(result["rotation_reason"], "new_session")
        manager.create.assert_awaited_once()


class HermesACPRuntimeTests(unittest.IsolatedAsyncioTestCase):
    def test_yolo_is_scoped_to_hermes_acp_process_arguments(self) -> None:
        with patch.dict("os.environ", {"VIEWER_HERMES_YOLO": "true", "VIEWER_HERMES_PROFILE": "default"}):
            runtime = HermesACPRuntime(AsyncMock())
        self.assertEqual(runtime._agent_arguments(), ["-p", "default", "--yolo", "acp"])

    def test_embedded_resource_content_block_is_validated(self) -> None:
        block = HermesACPRuntime._content_block(
            {
                "type": "resource",
                "resource": {"uri": "viewer://diff", "mimeType": "text/x-diff", "text": "+changed"},
            }
        )
        self.assertEqual(block.resource.uri, "viewer://diff")
        self.assertEqual(block.resource.text, "+changed")

        runtime = HermesACPRuntime(AsyncMock())
        runtime.agent_capabilities = AgentCapabilities(promptCapabilities=PromptCapabilities(image=True))
        fallback = runtime._supported_prompt_block(block)
        self.assertEqual(fallback.type, "text")
        self.assertIn("viewer://diff", fallback.text)

    async def test_null_load_response_is_not_marked_bound(self) -> None:
        runtime = HermesACPRuntime(AsyncMock())
        connection = SimpleNamespace(
            load_session=AsyncMock(
                return_value=SimpleNamespace(
                    field_meta=None,
                    config_options=None,
                    models=None,
                    modes=None,
                    model_fields_set=set(),
                )
            )
        )
        runtime.start = AsyncMock()
        runtime._require_connection = Mock(return_value=connection)

        with self.assertRaisesRegex(HermesACPSessionNotFound, "session not found"):
            await runtime.ensure_session("legacy-session", "/srv/projects/demo")

        self.assertNotIn("legacy-session", runtime._bound_sessions)


class GenericACPAbstractionTests(unittest.TestCase):
    def test_runtime_process_start_is_supplied_by_provider_config(self) -> None:
        runtime = ACPRuntime(
            ACPProcessConfig(
                provider="example",
                command="example-agent",
                arguments=("serve-acp", "--stdio"),
                profile="work",
            ),
            AsyncMock(),
        )

        self.assertEqual(runtime.provider, "example")
        self.assertEqual(runtime.command, "example-agent")
        self.assertEqual(runtime._agent_arguments(), ["serve-acp", "--stdio"])

    def test_session_manager_is_provider_neutral(self) -> None:
        runtime = SimpleNamespace(profile="work")
        manager = ACPSessionManager("example", runtime, Mock())

        self.assertEqual(manager.provider, "example")
        self.assertIs(manager.acp, runtime)

    def test_normalized_events_are_written_to_viewer_history_without_provider_source(self) -> None:
        runtime = SimpleNamespace(profile="work")
        manager = ACPSessionManager("example", runtime, Path("/tmp/example-acp-metadata"))
        manager._notify_lineage = Mock()
        session = ACPSession(
            provider="example",
            id="viewer-1",
            user_id="dailing",
            title="test",
            cwd="/srv/projects/demo",
            model=None,
            created_at=1,
            updated_at=1,
            provider_session_id="provider-1",
            lineage={
                "workspace_id": "workspace-1",
                "chat_id": "chat-1",
                "query_message_id": "query-1",
                "driver_run_id": "driver-1",
            },
        )
        event = {
            "event_type": "message:assistant",
            "text": "done",
            "received_at": 2,
            "source_event_id": "example-acp:provider-1:message",
            "streaming": False,
        }

        with patch("app.agent_history.agent_history_store.record_provider_message") as record:
            manager._record_lineage_events(session, [event], 0)

        self.assertEqual(record.call_args.kwargs["provider"], "example")
        self.assertIsNone(record.call_args.kwargs["source_path"])
        self.assertEqual(
            record.call_args.kwargs["source_event_id"],
            "dispatch:driver-1:example-acp:provider-1:message",
        )

    def test_reused_provider_event_ids_are_unique_per_dispatch(self) -> None:
        runtime = SimpleNamespace(profile="work")
        manager = ACPSessionManager("example", runtime, Path("/tmp/example-acp-metadata"))
        manager._notify_lineage = Mock()
        session = ACPSession(
            provider="example",
            id="viewer-1",
            user_id="dailing",
            title="test",
            cwd="/srv/projects/demo",
            model=None,
            created_at=1,
            updated_at=1,
            provider_session_id="provider-1",
            lineage={
                "workspace_id": "workspace-1",
                "chat_id": "chat-1",
                "query_message_id": "query-1",
                "driver_run_id": "driver-1",
            },
        )
        event = {
            "event_type": "message:assistant",
            "text": "done",
            "received_at": 2,
            "source_event_id": "example-acp:provider-1:agent_message_chunk:1",
            "streaming": False,
        }

        with patch("app.agent_history.agent_history_store.record_provider_message") as record:
            manager._record_lineage_events(session, [event], 0)
            session.lineage = {**session.lineage, "query_message_id": "query-2", "driver_run_id": "driver-2"}
            manager._record_lineage_events(session, [event], 0)

        source_ids = [call.kwargs["source_event_id"] for call in record.call_args_list]
        self.assertEqual(
            source_ids,
            [
                "dispatch:driver-1:example-acp:provider-1:agent_message_chunk:1",
                "dispatch:driver-2:example-acp:provider-1:agent_message_chunk:1",
            ],
        )


if __name__ == "__main__":
    unittest.main()
