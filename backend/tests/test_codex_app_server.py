import asyncio
import json
import os
import tempfile
import time
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from backend.app.codex_app_server import CodexAppServerRuntime, CodexAppServerProcessConfig
from backend.app.codex_app_server_sessions import CodexAppServerSessionManager, CodexAppServerSession
from backend.app.models import AgentEventType


class TestCodexAppServerRuntime:
    def test_config_defaults(self):
        config = CodexAppServerProcessConfig(
            provider="codex-app-server",
            command="codex",
            arguments=("app-server", "--stdio"),
        )
        assert config.provider == "codex-app-server"
        assert config.enabled is True

    def test_not_running_before_start(self):
        handler = AsyncMock()
        runtime = CodexAppServerRuntime(
            CodexAppServerProcessConfig(provider="codex-app-server", command="nonexistent", arguments=()),
            handler,
        )
        assert runtime.running is False
        with pytest.raises(RuntimeError):
            asyncio.run(runtime.thread_start("/tmp"))

    def test_request_timeout_discards_runtime_process(self):
        handler = AsyncMock()
        runtime = CodexAppServerRuntime(
            CodexAppServerProcessConfig(provider="codex-app-server", command="codex", arguments=()),
            handler,
        )
        runtime._process = MagicMock(returncode=None, stdin=MagicMock())
        runtime._write_message = AsyncMock()
        runtime._close_process = AsyncMock()

        async def run_request():
            with patch("backend.app.codex_app_server.asyncio.wait_for", new=AsyncMock(side_effect=asyncio.TimeoutError)):
                with pytest.raises(RuntimeError, match="request timed out: thread/start"):
                    await runtime._send_request("thread/start", {"cwd": "/tmp"})

        asyncio.run(run_request())

        runtime._close_process.assert_awaited_once()
        assert runtime._pending_requests == {}


class TestCodexAppServerSessionManager:
    def test_prompt_text_string(self):
        manager = CodexAppServerSessionManager()
        assert manager._prompt_text("hello world") == "hello world"

    def test_prompt_text_blocks(self):
        manager = CodexAppServerSessionManager()
        blocks = [
            {"type": "text", "text": "hello"},
            {"type": "image", "url": "https://example.com/img.png"},
            {"type": "localImage", "path": "/tmp/img.png"},
        ]
        result = manager._prompt_text(blocks)
        assert "hello" in result
        assert "[Image: https://example.com/img.png]" in result
        assert "[Local image: /tmp/img.png]" in result

    def test_title_for(self):
        manager = CodexAppServerSessionManager()
        assert manager._title_for("  hello   world  ") == "hello world"
        assert manager._title_for("") == "codex-app-server session"

    def test_normalized_event_agent_message_delta(self):
        manager = CodexAppServerSessionManager()
        session = CodexAppServerSession(
            provider="codex-app-server",
            id="test-id",
            user_id="dailing",
            title="test",
            cwd="/tmp",
            model=None,
            created_at=time.time(),
            updated_at=time.time(),
            provider_session_id="thread-123",
        )
        event = manager._normalized_event(
            session,
            "item/agentMessage/delta",
            {"delta": "Hello world", "threadId": "thread-123", "turnId": "turn-1", "itemId": "item-1"},
            {"method": "item/agentMessage/delta", "params": {"delta": "Hello world"}},
        )
        assert event is not None
        assert event["event_type"] == "message:assistant"
        assert event["text"] == "Hello world"
        assert event["append"] is True
        assert event["streaming"] is True

    def test_normalized_event_agent_thought_delta(self):
        manager = CodexAppServerSessionManager()
        session = CodexAppServerSession(
            provider="codex-app-server",
            id="test-id",
            user_id="dailing",
            title="test",
            cwd="/tmp",
            model=None,
            created_at=time.time(),
            updated_at=time.time(),
            provider_session_id="thread-123",
        )
        event = manager._normalized_event(
            session,
            "item/reasoning/summaryTextDelta",
            {"delta": "Thinking...", "threadId": "thread-123", "turnId": "turn-1", "itemId": "item-1"},
            {"method": "item/reasoning/summaryTextDelta", "params": {"delta": "Thinking..."}},
        )
        assert event is not None
        assert event["event_type"] == "reasoning"
        assert event["text"] == "Thinking..."
        assert event["append"] is True

    def test_normalized_event_turn_completed(self):
        manager = CodexAppServerSessionManager()
        session = CodexAppServerSession(
            provider="codex-app-server",
            id="test-id",
            user_id="dailing",
            title="test",
            cwd="/tmp",
            model=None,
            created_at=time.time(),
            updated_at=time.time(),
            provider_session_id="thread-123",
        )
        event = manager._normalized_event(
            session,
            "turn/completed",
            {"threadId": "thread-123", "turn": {"status": "completed", "id": "turn-1"}},
            {"method": "turn/completed", "params": {"turn": {"status": "completed"}}},
        )
        assert event is not None
        assert event["event_type"] == AgentEventType.SESSION_UPDATE
        assert event["streaming"] is False

    def test_normalized_event_turn_completed_failed(self):
        manager = CodexAppServerSessionManager()
        session = CodexAppServerSession(
            provider="codex-app-server",
            id="test-id",
            user_id="dailing",
            title="test",
            cwd="/tmp",
            model=None,
            created_at=time.time(),
            updated_at=time.time(),
            provider_session_id="thread-123",
        )
        event = manager._normalized_event(
            session,
            "turn/completed",
            {"threadId": "thread-123", "turn": {"id": "turn-1", "status": "failed", "error": {"message": "Something went wrong"}}},
            {"method": "turn/completed", "params": {"turn": {"status": "failed"}}},
        )
        assert event is not None
        assert event["text"] == "Something went wrong"

    def test_normalized_event_file_change_patch_updated(self):
        manager = CodexAppServerSessionManager()
        session = CodexAppServerSession(
            provider="codex-app-server",
            id="test-id",
            user_id="dailing",
            title="test",
            cwd="/tmp",
            model=None,
            created_at=time.time(),
            updated_at=time.time(),
            provider_session_id="thread-123",
        )
        event = manager._normalized_event(
            session,
            "item/fileChange/patchUpdated",
            {
                "threadId": "thread-123",
                "turnId": "turn-1",
                "itemId": "item-1",
                "changes": [{"path": "file.py", "diff": "--- a/file.py\n+++ b/file.py\n@@ -1 +1 @@\n-old\n+new", "kind": {"type": "update"}}],
            },
            {"method": "item/fileChange/patchUpdated", "params": {"changes": []}},
        )
        assert event is not None
        assert event["event_type"] == "patch_apply_end"
        assert event["patch_text"] is not None
        assert len(event["file_changes"]) == 1
        assert event["file_changes"][0]["path"] == "file.py"

    def test_normalized_event_token_usage_updated(self):
        manager = CodexAppServerSessionManager()
        session = CodexAppServerSession(
            provider="codex-app-server",
            id="test-id",
            user_id="dailing",
            title="test",
            cwd="/tmp",
            model=None,
            created_at=time.time(),
            updated_at=time.time(),
            provider_session_id="thread-123",
        )
        event = manager._normalized_event(
            session,
            "thread/tokenUsage/updated",
            {
                "threadId": "thread-123",
                "turnId": "turn-1",
                "tokenUsage": {
                    "total": {"totalTokens": 947721, "inputTokens": 942940, "outputTokens": 4781},
                    "last": {"totalTokens": 500},
                    "modelContextWindow": 250000,
                },
            },
            {"method": "thread/tokenUsage/updated", "params": {"tokenUsage": {"total": {"totalTokens": 947721}}}},
        )
        assert event is not None
        assert session.total_tokens == 500
        assert session.model_context_window == 250000
        assert session.summary()["context_used_percent"] == 0.2

    def test_streaming_deltas_upsert_by_codex_item_id(self):
        manager = CodexAppServerSessionManager()
        session = CodexAppServerSession(
            provider="codex-app-server",
            id="test-id",
            user_id="dailing",
            title="test",
            cwd="/tmp",
            model=None,
            created_at=time.time(),
            updated_at=time.time(),
            provider_session_id="thread-123",
        )
        first = manager._normalized_event(
            session,
            "item/agentMessage/delta",
            {"delta": "Hello ", "threadId": "thread-123", "turnId": "turn-1", "itemId": "item-1"},
            {},
        )
        second = manager._normalized_event(
            session,
            "item/agentMessage/delta",
            {"delta": "world", "threadId": "thread-123", "turnId": "turn-1", "itemId": "item-1"},
            {},
        )
        assert first is not None and second is not None
        manager._upsert_event(session, first)
        manager._upsert_event(session, second)
        assert len(session.events) == 1
        assert session.events[0]["text"] == "Hello world"

    def test_normalized_event_unknown_ignored(self):
        manager = CodexAppServerSessionManager()
        session = CodexAppServerSession(
            provider="codex-app-server",
            id="test-id",
            user_id="dailing",
            title="test",
            cwd="/tmp",
            model=None,
            created_at=time.time(),
            updated_at=time.time(),
            provider_session_id="thread-123",
        )
        event = manager._normalized_event(
            session,
            "unknownMethod",
            {"foo": "bar"},
            {"method": "unknownMethod", "params": {"foo": "bar"}},
        )
        assert event is None

    def test_raw_preview_small(self):
        manager = CodexAppServerSessionManager()
        raw = {"type": "test", "data": "small"}
        assert manager._raw_preview(raw) == raw

    def test_raw_preview_large(self):
        manager = CodexAppServerSessionManager()
        raw = {"type": "test", "data": "x" * 20000}
        preview = manager._raw_preview(raw)
        assert preview is not None
        assert preview["omitted_bytes"] > 0

    def test_event_for_detail_focus(self):
        manager = CodexAppServerSessionManager()
        event = {"event_type": "message:assistant", "text": "hello", "file_changes": [{"path": "a"}], "patch_text": "p", "raw_preview": {}}
        focused = manager._event_for_detail(event, "focus")
        assert focused is not None
        assert focused["file_changes"] == []
        assert focused["patch_text"] is None
        assert focused["raw_preview"] is None

    def test_event_for_detail_full(self):
        manager = CodexAppServerSessionManager()
        event = {"event_type": "message:assistant", "text": "hello", "file_changes": [{"path": "a"}], "patch_text": "p", "raw_preview": {}}
        full = manager._event_for_detail(event, "full")
        assert full == event

    def test_event_for_detail_non_message(self):
        manager = CodexAppServerSessionManager()
        event = {"event_type": "tool_call", "text": "hello"}
        assert manager._event_for_detail(event, "focus") is None
        assert manager._event_for_detail(event, "full") == event
