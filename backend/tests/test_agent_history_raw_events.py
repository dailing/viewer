import json
import tempfile
from pathlib import Path

from sqlalchemy import select

from backend.app.agent_history import AgentHistoryStore, SuperWorkspaceProviderRawEventRow


def test_provider_raw_event_archive_preserves_full_payload_and_is_idempotent() -> None:
    with tempfile.TemporaryDirectory() as directory:
        store = AgentHistoryStore(Path(directory) / "history.sqlite3")
        raw = {
            "method": "turn/diff/updated",
            "params": {"turnId": "provider-turn-1", "diff": "@@ -1 +1 @@\n-old\n+new"},
        }
        values = {
            "user_id": "dailing",
            "workspace_id": "workspace-1",
            "chat_id": "chat-1",
            "query_message_id": "query-1",
            "driver_run_id": "driver-1",
            "turn_id": "provider-turn-1",
            "provider": "codex-app-server",
            "viewer_session_id": "viewer-1",
            "provider_session_id": "provider-1",
            "event_index": 3,
            "event_method": "turn/diff/updated",
            "source_event_id": "raw-1",
            "received_at": 123.5,
            "raw": raw,
        }
        store.record_provider_raw_event(**values)
        store.record_provider_raw_event(**values)

        with store.read_session() as db:
            rows = list(db.scalars(select(SuperWorkspaceProviderRawEventRow)).all())

        assert len(rows) == 1
        assert json.loads(rows[0].raw_json) == raw
        assert rows[0].payload_bytes == len(json.dumps(raw, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))
        assert rows[0].driver_run_id == "driver-1"
