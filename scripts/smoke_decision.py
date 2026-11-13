#!/usr/bin/env python3
"""Persistent Decision marker, Auto Route dispatch, cursor list and cancellation smoke."""
from __future__ import annotations
import asyncio, json, sqlite3, sys, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from sdk import BusClient  # noqa: E402
from smoke_task_api import chat_server, make_workspace, dispatch, wait_for  # noqa: E402
GATEWAY = {"id": "gateway", "version": "1.0.0", "slots": {}, "emits": {}}
async def decisions(client: BusClient, task_id: str, **extra: object) -> dict:
    return await client.request("chat:_:decisions:list", {"task_id": task_id, "include_pending_count": True, "limit": 50, **extra})
def prompts(data_dir: Path) -> list[dict[str, str]]:
    path = data_dir.parent / "prompts.jsonl"
    return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []
async def run() -> None:
    async with chat_server() as (client, data_dir, completions):
        role, chat, task = await make_workspace(client)
        role = await client.request("chat:_:roles:patch", {"id": role["id"], "description": "handles Decision smoke follow-ups"})
        router_requests: list[dict[str, Any]] = []
        class RouterHandler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:
                request = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
                router_requests.append(request)
                content = json.dumps({"role_ids": [role["id"]], "rationale": "smoke Auto Route"})
                body = json.dumps({"choices": [{"message": {"role": "assistant", "content": content}}]}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            def log_message(self, format: str, *args: object) -> None:
                pass
        router = ThreadingHTTPServer(("127.0.0.1", 0), RouterHandler)
        router_thread = threading.Thread(target=router.serve_forever, daemon=True)
        router_thread.start()
        await client.request("config:_:set", {"plugin": "plugins.llm", "key": "active", "value": {"endpoint": f"http://127.0.0.1:{router.server_port}/v1", "model": "router-smoke"}})
        gateway = BusClient(client.url, GATEWAY, request_timeout=25.0)
        await gateway.connect()
        try:
            await dispatch(client, task["id"], "MOCK_DECISION first", role["id"], "decision-first")
            await wait_for(lambda: len(completions) >= 1)
            captured = prompts(data_dir)
            assert len(captured) == 1
            assert captured[0]["text"].index("MOCK_DECISION first") < captured[0]["text"].index("Decision follow-up protocol")
            assert captured[0]["text"].count("<viewer-decision>") == 1 and "option_action: end_current_task" in captured[0]["text"]
            pending = await decisions(client, task["id"], pending=True)
            assert pending["pending_count"] == 1 and len(pending["decisions"]) == 1
            decision = pending["decisions"][0]
            assert decision["status"] == "pending" and decision["question"] == "Continue with the recommended path?"
            assert decision["options"] == [
                {"id": "option-1", "label": "Recommended path"},
                {"id": "option-2", "label": "Alternative path"},
                {"id": "viewer-end-current-task", "label": "结束当前 Task", "action": "end_current_task"},
            ]
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
            context = await client.request("chat:_:decisions:context", {"decision_id": decision["id"]})
            assert context["source_turn_id"] == decision["source_turn_id"]
            assert [item["role"] for item in context["messages"]] == ["user", "assistant"]
            assert context["messages"][0]["text"] == "MOCK_DECISION first"
            assert context["messages"][1]["id"] == decision["source_message_id"] and context["messages"][1]["text"] == message["text"]
            assert all(item["turn_id"] in {decision["source_turn_id"], context["messages"][0]["turn_id"]} for item in context["messages"])
            try: await client.request("chat:_:decisions:respond", {"decision_id": decision["id"], "response": "Recommended path", "expected_version": decision["version"]})
            except Exception as exc: assert "gateway" in str(exc)
            else: raise AssertionError("non-gateway caller resolved a human Decision")
            response = {"decision_id": decision["id"], "response": "Recommended path", "option_id": "option-1", "expected_version": decision["version"]}
            resolved = await gateway.request("chat:_:decisions:respond", response)
            assert resolved["status"] == "routing" and resolved["role_ids"] == []
            assert resolved["decision"]["status"] == "resolved" and resolved["decision"]["response"] == "Recommended path"
            assert resolved["decision"]["resolved_dispatch_id"]
            replay = await gateway.request("chat:_:decisions:respond", response)
            assert replay["decision"]["resolved_dispatch_id"] == resolved["decision"]["resolved_dispatch_id"]
            await wait_for(lambda: len(completions) >= 2)
            assert any("Current message:\nRecommended path" in str(message.get("content", ""))
                       for request in router_requests for message in request.get("messages", []))
            captured = prompts(data_dir)
            assert len(captured) == 2 and captured[1]["session_id"] == captured[0]["session_id"]
            assert "While you were away" not in captured[1]["text"]
            assert captured[1]["text"].index("Recommended path") < captured[1]["text"].index("Decision follow-up protocol")
            assert captured[1]["text"].count("<viewer-decision>") == 1
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
            await client.request("chat:_:send-message", {"task_id": task["id"], "role_ids": [role["id"]], "text": "raw independent", "raw": True, "idempotency_key": "decision-raw"})
            await wait_for(lambda: len(completions) >= 5)
            captured = prompts(data_dir)
            assert captured[-1]["text"] == "raw independent" and "<viewer-decision>" not in captured[-1]["text"]
            await dispatch(client, task["id"], "MOCK_DECISION third", role["id"], "decision-third")
            await wait_for(lambda: len(completions) >= 6)
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
            end_task = (await client.request("chat:_:tasks:create", {"chat_id": chat["id"], "title": "Decision ending", "idempotency_key": "decision-end-task-create"}))["task"]
            await dispatch(client, end_task["id"], "MOCK_END_TASK_DECISION", role["id"], "decision-end-task")
            await wait_for(lambda: len(completions) >= 7)
            ending = (await decisions(client, end_task["id"], pending=True))["decisions"][0]
            assert ending["options"][1] == {"id": "option-2", "label": "End current task", "action": "end_current_task"}
            end_response = {"decision_id": ending["id"], "response": "End current task", "option_id": "option-2", "expected_version": ending["version"]}
            before_end = len(completions)
            ended_by_decision = await gateway.request("chat:_:decisions:respond", end_response)
            assert ended_by_decision["task"]["status"] == "ended"
            assert ended_by_decision["decision"]["status"] == "resolved" and ended_by_decision["decision"]["response"] == "End current task"
            assert not ended_by_decision["decision"]["resolved_dispatch_id"]
            replay_end = await gateway.request("chat:_:decisions:respond", end_response)
            assert replay_end["decision"]["id"] == ending["id"] and replay_end["task"]["status"] == "ended"
            await asyncio.sleep(0.2)
            assert len(completions) == before_end
            database = sqlite3.connect(data_dir / "chat.sqlite3")
            try:
                assert database.execute("select count(*) from turns where task_id=?", (end_task["id"],)).fetchone()[0] == 1
                assert database.execute("select count(*) from messages where task_id=? and role='user' and text='End current task'", (end_task["id"],)).fetchone()[0] == 0
            finally: database.close()
            generated_end_task = (await client.request("chat:_:tasks:create", {"chat_id": chat["id"], "title": "Generated Decision ending", "idempotency_key": "decision-generated-end-task-create"}))["task"]
            await dispatch(client, generated_end_task["id"], "MOCK_DECISION generated end", role["id"], "decision-generated-end-task")
            await wait_for(lambda: len(completions) >= 8)
            generated = (await decisions(client, generated_end_task["id"], pending=True))["decisions"][0]
            generated_end = generated["options"][-1]
            assert generated_end == {"id": "viewer-end-current-task", "label": "结束当前 Task", "action": "end_current_task"}
            before_generated_end = len(completions)
            generated_result = await gateway.request("chat:_:decisions:respond", {
                "decision_id": generated["id"], "response": generated_end["label"],
                "option_id": generated_end["id"], "expected_version": generated["version"],
            })
            assert generated_result["task"]["status"] == "ended"
            assert generated_result["decision"]["status"] == "resolved" and generated_result["decision"]["response"] == "结束当前 Task"
            await asyncio.sleep(0.2)
            assert len(completions) == before_generated_end
            database = sqlite3.connect(data_dir / "chat.sqlite3")
            try:
                assert database.execute("select count(*) from turns where task_id=?", (generated_end_task["id"],)).fetchone()[0] == 1
                assert database.execute("select count(*) from messages where task_id=? and role='user' and text=?", (generated_end_task["id"], "结束当前 Task")).fetchone()[0] == 0
            finally: database.close()
            print("PASS Decision per-Turn prompt/raw boundary, parse/strip, visible source-Turn context, gateway response through async Auto Route, built-in and Agent-provided direct Task end, idempotency and cursor list")
        finally:
            await gateway.close()
            router.shutdown()
            router.server_close()
            router_thread.join(timeout=2)
def main() -> None: asyncio.run(run()); print("PASS Decision smoke complete")
if __name__ == "__main__": main()
