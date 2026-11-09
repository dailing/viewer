#!/usr/bin/env python3
"""Persistent Decision marker, continuation, cursor list and cancellation smoke."""
from __future__ import annotations
import asyncio, sqlite3, sys
from pathlib import Path
ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from sdk import BusClient  # noqa: E402
from smoke_task_api import chat_server, make_workspace, dispatch, wait_for  # noqa: E402
GATEWAY = {"id": "gateway", "version": "1.0.0", "slots": {}, "emits": {}}
async def decisions(client: BusClient, task_id: str, **extra: object) -> dict:
    return await client.request("chat:_:decisions:list", {"task_id": task_id, "include_pending_count": True, "limit": 50, **extra})
async def run() -> None:
    async with chat_server() as (client, data_dir, completions):
        role, chat, task = await make_workspace(client)
        gateway = BusClient(client.url, GATEWAY, request_timeout=25.0)
        await gateway.connect()
        try:
            await dispatch(client, task["id"], "MOCK_DECISION first", role["id"], "decision-first")
            await wait_for(lambda: len(completions) >= 1)
            pending = await decisions(client, task["id"], pending=True)
            assert pending["pending_count"] == 1 and len(pending["decisions"]) == 1
            decision = pending["decisions"][0]
            assert decision["status"] == "pending" and decision["question"] == "Continue with the recommended path?"
            assert decision["options"] == [{"id": "option-1", "label": "Recommended path"}, {"id": "option-2", "label": "Alternative path"}]
            start, end = decision["marker_start"], decision["marker_end"]
            history = await client.request("chat:_:history", {"task_id": task["id"], "include_messages": True, "limit": 50})
            message = next(item for item in history["messages"] if item["id"] == decision["source_message_id"])
            raw = message["text"].encode()
            assert raw[start:end].decode().startswith("<viewer-decision>") and raw[start:end].decode().endswith("</viewer-decision>")
            assert raw[:start].decode() == "请选择后续方向：\n"
            assert start >= 0 and end > start
            # History preserves the source bytes; Task UI and model context use these offsets to omit the marker.
            assert message["task_id"] == task["id"]
            opened = await gateway.request("chat:_:decisions:open", {"decision_id": decision["id"]})
            assert opened["source_message_id"] == message["id"] and opened["task_id"] == task["id"]
            try: await client.request("chat:_:decisions:respond", {"decision_id": decision["id"], "response": "Recommended path", "expected_version": decision["version"]})
            except Exception as exc: assert "gateway" in str(exc)
            else: raise AssertionError("non-gateway caller resolved a human Decision")
            response = {"decision_id": decision["id"], "response": "Recommended path", "expected_version": decision["version"]}
            resolved = await gateway.request("chat:_:decisions:respond", response)
            assert resolved["decision"]["status"] == "resolved" and resolved["decision"]["response"] == "Recommended path"
            assert resolved["decision"]["resolved_dispatch_id"]
            replay = await gateway.request("chat:_:decisions:respond", response)
            assert replay["decision"]["resolved_dispatch_id"] == resolved["decision"]["resolved_dispatch_id"]
            await wait_for(lambda: len(completions) >= 2)
            history_decisions = await decisions(client, task["id"], history=True)
            stored = next(item for item in history_decisions["decisions"] if item["id"] == decision["id"])
            assert stored["status"] == "resolved" and stored["response"] == "Recommended path"
            database = sqlite3.connect(data_dir / "chat.sqlite3")
            try:
                assert database.execute("select count(*) from role_sessions where task_id=? and role_id=?", (task["id"], role["id"])).fetchone()[0] == 1
                reply_count = database.execute("select count(*) from messages where task_id=? and role='user' and text=?", (task["id"], "Recommended path")).fetchone()[0]
                assert reply_count == 1
                assert stored["resolved_dispatch_id"] == resolved["decision"]["resolved_dispatch_id"]
            finally: database.close()
            await dispatch(client, task["id"], "MOCK_DECISION second", role["id"], "decision-second")
            await wait_for(lambda: len(completions) >= 3)
            second = (await decisions(client, task["id"], pending=True))["decisions"][0]
            # An ordinary reply with no decision_id auto-associates only the unique pending Decision.
            ordinary = await dispatch(client, task["id"], "ordinary automatic reply", role["id"], "decision-ordinary-auto")
            await wait_for(lambda: len(completions) >= 4)
            auto_resolved = next(item for item in (await decisions(client, task["id"], history=True))["decisions"] if item["id"] == second["id"])
            assert auto_resolved["status"] == "resolved" and auto_resolved["response"] == "ordinary automatic reply"
            assert auto_resolved["resolved_dispatch_id"] == ordinary["dispatch_id"] and ordinary["role_ids"] == [role["id"]]
            await dispatch(client, task["id"], "MOCK_DECISION third", role["id"], "decision-third")
            await wait_for(lambda: len(completions) >= 5)
            third = (await decisions(client, task["id"], pending=True))["decisions"][0]
            ended = await client.request("chat:_:tasks:end", {"task_id": task["id"]})
            assert ended["task"]["status"] == "ended"
            cancelled = next(item for item in (await decisions(client, task["id"], history=True))["decisions"] if item["id"] == third["id"])
            assert cancelled["status"] == "cancelled"
            try: await gateway.request("chat:_:decisions:respond", {"decision_id": third["id"], "response": "too late", "expected_version": third["version"]})
            except Exception: pass
            else: raise AssertionError("ended Task Decision remained actionable")
            # Cursor pagination is stable and does not repeat the boundary row.
            full = await decisions(client, task["id"], history=True, limit=1)
            assert full["has_more"] and full["next_created_at"]
            following = await decisions(client, task["id"], history=True, limit=1, before_created_at=full["next_created_at"], before_id=full["next_id"])
            assert full["decisions"][0]["id"] != following["decisions"][0]["id"]
            print("PASS Decision parse/strip, gateway-only response, same Role/session continuation, idempotency, ended cancel and cursor list")
        finally: await gateway.close()
def main() -> None: asyncio.run(run()); print("PASS Decision smoke complete")
if __name__ == "__main__": main()
