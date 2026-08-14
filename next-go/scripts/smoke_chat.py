#!/usr/bin/env python3
"""Black-box M6b workspace/chat/dual-role relay smoke through the Go bus."""
from __future__ import annotations
import asyncio, json, os, socket, sqlite3, subprocess, sys, tempfile, time
from pathlib import Path
from typing import Any, Callable

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(os.environ.get("VIEWER_SMOKE_NEXT", ROOT / "next"))))
from sdk import BusClient  # noqa: E402

CALLER = {"id": "chat-smoke", "version": "0.2.0", "slots": {}, "emits": {}}
def free_port() -> int:
    with socket.socket() as sock: sock.bind(("127.0.0.1", 0)); return int(sock.getsockname()[1])
async def wait_for(probe: Callable[[], Any], timeout: float = 20.0) -> Any:
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        if value := probe(): return value
        await asyncio.sleep(0.03)
    raise TimeoutError("chat smoke condition timed out")
async def wait_port(port: int) -> None:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            _, writer = await asyncio.open_connection("127.0.0.1", port); writer.close(); await writer.wait_closed(); return
        except OSError: await asyncio.sleep(0.05)
    raise TimeoutError(f"kernel port {port} did not open")

async def run() -> None:
    viewerd, mock, port = Path(os.environ["VIEWERD_BIN"]), Path(__file__).with_name("mock_acp_agent.py"), free_port()
    with tempfile.TemporaryDirectory(prefix="viewer-chat-smoke-") as temp:
        data_dir, log_path = Path(temp) / "data", Path(temp) / "viewerd.log"
        environment = {**os.environ, "VIEWER_HERMES_COMMAND": str(mock), "VIEWER_HERMES_PROFILE": "mock-profile", "VIEWER_HERMES_YOLO": "true"}
        with log_path.open("wb") as log:
            process = subprocess.Popen([str(viewerd), "--plugins=config-store,viewer.agent-hermes,chat", "--kernel-port", str(port), "--data-dir", str(data_dir)], env=environment, stdout=log, stderr=subprocess.STDOUT)
        client = BusClient(f"ws://127.0.0.1:{port}/ws", CALLER, request_timeout=25.0)
        messages: list[dict[str, Any]] = []; completions: list[dict[str, Any]] = []; active: list[Any] = []; catalogs: list[Any] = []; agent_events: list[dict[str, Any]] = []
        try:
            await wait_port(port)
            registry: list[list[dict[str, Any]]] = []
            async def collect_messages(frame: dict[str, Any]) -> None: messages.append(frame["value"])
            async def collect_completions(frame: dict[str, Any]) -> None: completions.append(frame["value"])
            async def collect_active(frame: dict[str, Any]) -> None: active.append(frame["value"])
            async def collect_registry(frame: dict[str, Any]) -> None: registry.append(frame["value"])
            async def collect_catalog(frame: dict[str, Any]) -> None: catalogs.append(frame["value"])
            async def collect_agent_event(frame: dict[str, Any]) -> None: agent_events.append(frame["value"])
            await client.subscribe("chat:*:message", collect_messages)
            await client.subscribe("chat:*:turn-completed", collect_completions)
            await client.subscribe("chat:_:active", collect_active)
            await client.subscribe("plugins:_:list", collect_registry)
            await client.subscribe("viewer.agent-hermes:_:catalog", collect_catalog)
            await client.subscribe("viewer.agent-hermes:_:event", collect_agent_event)
            await client.connect()
            await wait_for(lambda: registry and {item["manifest"]["id"] for item in registry[-1]} >= {"chat", "config-store", "viewer.agent-hermes"})
            await wait_for(lambda: catalogs and catalogs[-1] and catalogs[-1]["agent"] == "hermes")
            print("CATALOG_MAILBOX_SAMPLE", json.dumps(catalogs[-1], separators=(",", ":")))
            routing = {"default_routing_policy_id": "hermes-policy", "routing_policies": [{"id": "hermes-policy", "name": "Hermes", "enabled": True, "auto_failover": True, "max_attempts": 2, "candidates": [{"id": "hermes-default", "name": "Hermes default", "agent_id": "hermes", "provider_id": "default", "model_id": "", "enabled": True, "parameters": {"profile": "mock-profile"}}]}]}
            await client.request("chat:_:routing:put", routing)
            first = await client.request("chat:_:roles:create", {"name": "Planner", "description": "plans", "prompt": "PLAN-RULE", "provider": "hermes", "routing_policy_id": "hermes-policy"})
            second = await client.request("chat:_:roles:create", {"name": "Builder", "description": "builds", "prompt": "BUILD-RULE", "provider": "hermes", "routing_policy_id": "hermes-policy"})
            await client.request("chat:_:workspace:patch", {"common_prompt": "COMMON-RULE"})
            chat = await client.request("chat:_:chats:create", {"name": "Relay", "root": str(ROOT), "type": "group", "member_role_ids": [first["id"], second["id"]], "common_prompt": "CHAT-RULE"})
            activated = await client.request("chat:_:chats:activate", {"id": chat["id"]})
            assert activated["id"] == chat["id"]
            await wait_for(lambda: active and active[-1] == chat["id"])
            print("PASS workspace roles and chat CRUD + active mailbox")

            dispatched = await client.request("chat:_:dispatch", {"chat_id": chat["id"], "message": "relay hello", "role_ids": [first["id"], second["id"]]})
            assert dispatched["role_ids"] == [first["id"], second["id"]]
            print("POLICY_ROUTE_SAMPLE", json.dumps({"policy_id": "hermes-policy", "candidate": "hermes/default", "dispatch_id": dispatched["dispatch_id"]}, separators=(",", ":")))
            await wait_for(lambda: len([item for item in completions if item["chat_id"] == chat["id"]]) == 2)
            done = [item for item in completions if item["chat_id"] == chat["id"]]
            assert [item["role_id"] for item in done] == [first["id"], second["id"]]
            assert all(item["sender"]["from"] == "role" and item["sender"]["role_name"] for item in done)
            chat_messages = [item for item in messages if item["chat_id"] == chat["id"]]
            assert chat_messages[0]["sender"] == {"from": "user"}
            assert [item["sender"].get("role_id") for item in chat_messages if item["role"] == "assistant"] == [first["id"], first["id"], second["id"], second["id"]]
            assert "COMMON-RULE" in "".join(item["text"] for item in chat_messages) and "BUILD-RULE" in "".join(item["text"] for item in chat_messages)
            database = sqlite3.connect(data_dir / "chat.sqlite3")
            try:
                assert database.execute("select count(*) from chats").fetchone()[0] == 1
                assert database.execute("select count(*) from role_sessions").fetchone()[0] == 2
                assert database.execute("select count(*) from turns").fetchone()[0] == 2
                assert database.execute("select count(*) from messages").fetchone()[0] == 5
                assert database.execute("select count(distinct role_id) from role_sessions where chat_id=?", (chat["id"],)).fetchone()[0] == 2
                raw_rows = database.execute("select raw_json from turn_events where chat_id=? order by turn_id,seq", (chat["id"],)).fetchall()
                assert len(raw_rows) >= 6  # mock emits tool_call + two text updates for each Role turn
                assert all(isinstance(json.loads(row[0]), dict) for row in raw_rows)
                block_row = database.execute("select kind,payload from message_blocks where chat_id=? and kind='tool_call' order by occurred_at limit 1", (chat["id"],)).fetchone()
                assert block_row and json.loads(block_row[1])["name"] == "Read fixture"
                print("RAW_JSON_SAMPLE", raw_rows[0][0])
                print("MESSAGE_BLOCK_SAMPLE", json.dumps({"kind": block_row[0], "payload": json.loads(block_row[1])}, separators=(",", ":")))
            finally: database.close()
            print("PASS explicit dual-role ordered relay, sender frames, raw events, parsed blocks, and DB rows")

            aggregate = await client.request("chat:_:agent-catalog", {})
            assert any(item["agent"] == "hermes" and item["online"] and item["providers"] for item in aggregate)
            print("AGGREGATE_CATALOG_SAMPLE", json.dumps(aggregate, separators=(",", ":")))

            prior_events = len(agent_events)
            await client.request("chat:_:dispatch", {"chat_id": chat["id"], "message": "long cancellation", "role_ids": [first["id"]]})
            await wait_for(lambda: len(agent_events) > prior_events)
            stopped = await client.request("chat:_:stop", {"chat_id": chat["id"], "role_id": first["id"]})
            assert stopped["stopped"] is True
            await wait_for(lambda: len(completions) >= 3 and completions[-1]["stop_reason"] == "cancelled")
            stopped_again = await client.request("chat:_:stop", {"chat_id": chat["id"], "role_id": first["id"]})
            assert stopped_again["stopped"] is False
            print("AGENT_EVENT_SAMPLE", json.dumps(agent_events[0], separators=(",", ":")))
            print("PASS routing policy, catalog aggregation, async agent event, and idempotent stop")

            try: await client.request("chat:_:dispatch", {"chat_id": chat["id"], "message": "auto"})
            except Exception as exc: assert "LLM router is not configured" in str(exc)
            else: raise AssertionError("router without config unexpectedly succeeded")
            print("PASS router missing-config error")
        except Exception:
            if log_path.exists(): print(log_path.read_text(errors="replace")[-4000:], file=sys.stderr)
            raise
        finally:
            await client.close(); process.terminate()
            try: process.wait(timeout=10)
            except subprocess.TimeoutExpired: process.kill(); process.wait(timeout=5)
            if process.returncode not in {0, -15}: raise RuntimeError(f"viewerd exited {process.returncode}:\n{log_path.read_text(errors='replace')[-4000:]}")

def main() -> None: asyncio.run(run()); print("PASS chat smoke complete")
if __name__ == "__main__": main()
