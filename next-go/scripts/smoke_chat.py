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
    viewerd, mock, codex_mock, opencode_mock, port = Path(os.environ["VIEWERD_BIN"]), Path(__file__).with_name("mock_acp_agent.py"), Path(__file__).with_name("mock_codex_server.py"), Path(__file__).with_name("mock_opencode_agent.py"), free_port()
    with tempfile.TemporaryDirectory(prefix="viewer-chat-smoke-") as temp:
        data_dir, log_path = Path(temp) / "data", Path(temp) / "viewerd.log"
        environment = {**os.environ, "VIEWER_HERMES_COMMAND": str(mock), "VIEWER_HERMES_PROFILE": "mock-profile", "VIEWER_HERMES_YOLO": "true", "VIEWER_CODEX_APP_SERVER_COMMAND": str(codex_mock), "VIEWER_CODEX_APP_SERVER_YOLO": "true", "VIEWER_OPENCODE_COMMAND": str(opencode_mock), "VIEWER_OPENCODE_ARGS": "acp"}
        with log_path.open("wb") as log:
            process = subprocess.Popen([str(viewerd), "--plugins=config-store,viewer.agent-hermes,viewer.agent-codex,viewer.agent-opencode,chat", "--kernel-port", str(port), "--data-dir", str(data_dir)], env=environment, stdout=log, stderr=subprocess.STDOUT)
        client = BusClient(f"ws://127.0.0.1:{port}/ws", CALLER, request_timeout=25.0)
        messages: list[dict[str, Any]] = []; completions: list[dict[str, Any]] = []; active: list[Any] = []; catalogs: list[Any] = []; codex_catalogs: list[Any] = []; opencode_catalogs: list[Any] = []; agent_events: list[dict[str, Any]] = []; codex_events: list[dict[str, Any]] = []; opencode_events: list[dict[str, Any]] = []
        try:
            await wait_port(port)
            registry: list[list[dict[str, Any]]] = []
            async def collect_messages(frame: dict[str, Any]) -> None: messages.append(frame["value"])
            async def collect_completions(frame: dict[str, Any]) -> None: completions.append(frame["value"])
            async def collect_active(frame: dict[str, Any]) -> None: active.append(frame["value"])
            async def collect_registry(frame: dict[str, Any]) -> None: registry.append(frame["value"])
            async def collect_catalog(frame: dict[str, Any]) -> None: catalogs.append(frame["value"])
            async def collect_codex_catalog(frame: dict[str, Any]) -> None: codex_catalogs.append(frame["value"])
            async def collect_opencode_catalog(frame: dict[str, Any]) -> None: opencode_catalogs.append(frame["value"])
            async def collect_agent_event(frame: dict[str, Any]) -> None: agent_events.append(frame["value"])
            async def collect_codex_event(frame: dict[str, Any]) -> None: codex_events.append(frame["value"])
            async def collect_opencode_event(frame: dict[str, Any]) -> None: opencode_events.append(frame["value"])
            await client.subscribe("chat:*:message", collect_messages)
            await client.subscribe("chat:*:turn-completed", collect_completions)
            await client.subscribe("chat:_:active", collect_active)
            await client.subscribe("plugins:_:list", collect_registry)
            await client.subscribe("viewer.agent-hermes:_:catalog", collect_catalog)
            await client.subscribe("viewer.agent-hermes:_:event", collect_agent_event)
            await client.subscribe("viewer.agent-codex:_:catalog", collect_codex_catalog)
            await client.subscribe("viewer.agent-codex:_:event", collect_codex_event)
            await client.subscribe("viewer.agent-opencode:_:catalog", collect_opencode_catalog)
            await client.subscribe("viewer.agent-opencode:_:event", collect_opencode_event)
            await client.connect()
            await wait_for(lambda: registry and {item["manifest"]["id"] for item in registry[-1]} >= {"chat", "config-store", "viewer.agent-hermes", "viewer.agent-codex", "viewer.agent-opencode"})
            await wait_for(lambda: catalogs and catalogs[-1] and catalogs[-1]["agent"] == "hermes")
            await wait_for(lambda: codex_catalogs and codex_catalogs[-1] and codex_catalogs[-1]["agent"] == "codex")
            await wait_for(lambda: opencode_catalogs and opencode_catalogs[-1] and opencode_catalogs[-1]["agent"] == "opencode")
            print("CATALOG_MAILBOX_SAMPLE", json.dumps(catalogs[-1], separators=(",", ":")))
            print("CODEX_CATALOG_MAILBOX_SAMPLE", json.dumps(codex_catalogs[-1], separators=(",", ":")))
            print("OPENCODE_CATALOG_MAILBOX_SAMPLE", json.dumps(opencode_catalogs[-1], separators=(",", ":")))
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
                assert database.execute("select count(*) from messages").fetchone()[0] == 3
                # streaming deltas aggregate into one message per role turn (was one row per delta)
                texts = [row[0] for row in database.execute("select text from messages where role='assistant' order by created_at").fetchall()]
                assert all(text.startswith("mock: ") and len(text) > 20 for text in texts)
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
            assert any(item["agent"] == "opencode" and item["online"] and item["providers"] for item in aggregate)
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

            routing["routing_policies"].extend([
                {"id": "codex-policy", "name": "Codex", "enabled": True, "auto_failover": False, "max_attempts": 0, "candidates": [{"id": "codex-default", "name": "Codex mock", "agent_id": "codex-app-server", "provider_id": "openai-subscription", "model_id": "gpt-test", "enabled": True, "parameters": {}}]},
                {"id": "failover-policy", "name": "Failover", "enabled": True, "auto_failover": True, "max_attempts": 2, "candidates": [{"id": "codex-fails", "name": "Failing Codex", "agent_id": "codex-app-server", "provider_id": "openai-subscription", "model_id": "fail-start", "enabled": True, "parameters": {}}, {"id": "hermes-fallback", "name": "Hermes fallback", "agent_id": "hermes", "provider_id": "default", "model_id": "", "enabled": True, "parameters": {"profile": "mock-profile"}}]},
                {"id": "turn-error-policy", "name": "Turn error failover", "enabled": True, "auto_failover": True, "max_attempts": 2, "candidates": [{"id": "codex-turn-error", "name": "Codex turn error", "agent_id": "codex-app-server", "provider_id": "openai-subscription", "model_id": "gpt-test", "enabled": True, "parameters": {}}, {"id": "hermes-after-turn-error", "name": "Hermes fallback", "agent_id": "hermes", "provider_id": "default", "model_id": "", "enabled": True, "parameters": {"profile": "mock-profile"}}]},
                {"id": "opencode-policy", "name": "OpenCode", "enabled": True, "auto_failover": False, "max_attempts": 1, "candidates": [{"id": "opencode-default", "name": "OpenCode mock", "agent_id": "opencode", "provider_id": "default", "model_id": "", "enabled": True, "parameters": {}}]},
            ])
            await client.request("chat:_:routing:put", routing)

            codex_role = await client.request("chat:_:roles:create", {"name": "Codex", "description": "codex", "prompt": "CODEX-RULE", "provider": "codex-app-server", "routing_policy_id": "codex-policy"})
            codex_chat = await client.request("chat:_:chats:create", {"name": "Codex bus", "root": str(ROOT), "type": "direct", "member_role_ids": [codex_role["id"]]})
            await client.request("chat:_:dispatch", {"chat_id": codex_chat["id"], "message": "codex hello", "role_ids": [codex_role["id"]]})
            await wait_for(lambda: any(item["chat_id"] == codex_chat["id"] for item in completions))
            codex_done = next(item for item in completions if item["chat_id"] == codex_chat["id"])
            assert codex_done["stop_reason"] == "end_turn" and codex_done["attempts"][0]["outcome"] == "completed"
            await wait_for(lambda: any(item["chat_id"] == codex_chat["id"] and item.get("text") == "mock answer" for item in messages))
            assert codex_events and any(item["block"]["kind"] == "agent_text" for item in codex_events)
            database = sqlite3.connect(data_dir / "chat.sqlite3")
            try:
                assert database.execute("select count(*) from turn_events where chat_id=? and provider='codex-app-server/openai-subscription'", (codex_chat["id"],)).fetchone()[0] >= 5
                assert database.execute("select count(*) from message_blocks where chat_id=? and kind='agent_text'", (codex_chat["id"],)).fetchone()[0] == 1
            finally: database.close()
            print("CODEX_BUS_EVENT_SAMPLE", json.dumps(next(item for item in codex_events if item["block"]["kind"] == "agent_text"), separators=(",", ":")))
            print("PASS codex bus start -> prompt -> event persistence -> turn-ended")

            opencode_role = await client.request("chat:_:roles:create", {"name": "OpenCode", "description": "opencode", "prompt": "OPENCODE-RULE", "provider": "opencode", "routing_policy_id": "opencode-policy"})
            opencode_chat = await client.request("chat:_:chats:create", {"name": "OpenCode bus", "root": str(ROOT), "type": "direct", "member_role_ids": [opencode_role["id"]]})
            await client.request("chat:_:dispatch", {"chat_id": opencode_chat["id"], "message": "opencode hello", "role_ids": [opencode_role["id"]]})
            await wait_for(lambda: any(item["chat_id"] == opencode_chat["id"] for item in completions))
            opencode_done = next(item for item in completions if item["chat_id"] == opencode_chat["id"])
            assert opencode_done["stop_reason"] == "end_turn" and opencode_done["attempts"][0]["outcome"] == "completed"
            await wait_for(lambda: any(item["chat_id"] == opencode_chat["id"] and item.get("text") == "opencode: " for item in messages))
            assert any(item["kind"] == "opencode_step" and item["block"]["kind"] == "other" and json.loads(item["block"]["payload"])["session_update"] == "opencode_step" for item in opencode_events)
            database = sqlite3.connect(data_dir / "chat.sqlite3")
            try:
                assert database.execute("select count(*) from turn_events where chat_id=? and provider='opencode/default'", (opencode_chat["id"],)).fetchone()[0] >= 3
                assert database.execute("select count(*) from message_blocks where chat_id=? and kind='other'", (opencode_chat["id"],)).fetchone()[0] >= 1
            finally: database.close()
            print("OPENCODE_BUS_EVENT_SAMPLE", json.dumps(next(item for item in opencode_events if item["kind"] == "opencode_step"), separators=(",", ":")))
            print("PASS opencode bus start -> prompt -> event persistence -> turn-ended")

            failover_role = await client.request("chat:_:roles:create", {"name": "Fallback", "description": "fallback", "prompt": "FALLBACK-RULE", "provider": "codex-app-server", "routing_policy_id": "failover-policy"})
            failover_chat = await client.request("chat:_:chats:create", {"name": "Failover", "root": str(ROOT), "type": "direct", "member_role_ids": [failover_role["id"]]})
            await client.request("chat:_:dispatch", {"chat_id": failover_chat["id"], "message": "fallback hello", "role_ids": [failover_role["id"]]})
            await wait_for(lambda: any(item["chat_id"] == failover_chat["id"] for item in completions))
            failover_done = next(item for item in completions if item["chat_id"] == failover_chat["id"])
            assert failover_done["stop_reason"] == "end_turn"
            assert [item["outcome"] for item in failover_done["attempts"]] == ["start_error", "completed"]
            assert [item["agent"] for item in failover_done["attempts"]] == ["codex-app-server", "hermes"]
            print("FAILOVER_ATTEMPTS_SAMPLE", json.dumps(failover_done["attempts"], separators=(",", ":")))
            print("PASS automatic failover advances after start RPC failure")

            turn_error_role = await client.request("chat:_:roles:create", {"name": "Turn error", "description": "turn error", "prompt": "TURN-ERROR-RULE", "provider": "codex-app-server", "routing_policy_id": "turn-error-policy"})
            turn_error_chat = await client.request("chat:_:chats:create", {"name": "Turn error failover", "root": str(ROOT), "type": "direct", "member_role_ids": [turn_error_role["id"]]})
            await client.request("chat:_:dispatch", {"chat_id": turn_error_chat["id"], "message": "mock turn error", "role_ids": [turn_error_role["id"]]})
            await wait_for(lambda: any(item["chat_id"] == turn_error_chat["id"] for item in completions))
            turn_error_done = next(item for item in completions if item["chat_id"] == turn_error_chat["id"])
            assert turn_error_done["stop_reason"] == "end_turn"
            assert [item["outcome"] for item in turn_error_done["attempts"]] == ["turn_error", "completed"]
            print("TURN_ERROR_FAILOVER_SAMPLE", json.dumps(turn_error_done["attempts"], separators=(",", ":")))
            print("PASS automatic failover advances after turn-ended error")

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
