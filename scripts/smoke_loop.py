#!/usr/bin/env python3
"""Black-box loop orchestration against real chat + a deterministic ACP agent."""
from __future__ import annotations
import asyncio
import json
import os
import sqlite3
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from sdk import BusClient
from smoke_chat import free_port, wait_port

async def run() -> None:
    port = free_port()
    with tempfile.TemporaryDirectory(prefix="viewer-loop-smoke-") as tmp:
        root = Path(tmp)
        env = {**os.environ, "VIEWER_HERMES_COMMAND": str(ROOT / "scripts/mock_acp_agent.py"), "VIEWER_HERMES_PROFILE": "mock-profile", "VIEWER_HERMES_YOLO": "true"}
        with (root / "server.log").open("wb") as log:
            process = subprocess.Popen([os.environ["VIEWERD_BIN"], "--plugins=config-store,viewer.agent-hermes,chat,viewer.loop", "--kernel-port", str(port), "--data-dir", str(root / "data")], env=env, stdout=log, stderr=subprocess.STDOUT)
        client = BusClient(f"ws://127.0.0.1:{port}/ws", {"id": "loop-smoke", "version": "1.0.0", "slots": {"llm:_:complete": {}}, "emits": {}}, request_timeout=15)
        verdicts = 0
        async def judge(frame):
            nonlocal verdicts
            value = frame["value"]
            if value.get("_cancel"):
                return
            # Chat summaries also use llm. Only the loop's judge increments.
            if "目标验收判定器" in value["messages"][0]["content"]:
                verdicts += 1
                content = json.dumps({"status": "continue" if verdicts == 1 else "complete", "reason": "mock verified evidence", "feedback": "next step", "progress": True})
            else:
                content = "mock summary"
            await client.publish(value["_reply_to"], {"_corr": value["_corr"], "ok": True, "result": {"content": content, "model": "mock"}})
        async def until_state(run_id, states, seconds=45):
            deadline = asyncio.get_running_loop().time() + seconds
            while asyncio.get_running_loop().time() < deadline:
                detail = await client.request("loop:_:get", {"id": run_id})
                if detail["loop"]["state"] in states:
                    return detail
                await asyncio.sleep(.15)
            raise AssertionError(f"loop did not reach {states}: {detail}")
        try:
            await wait_port(port)
            await client.subscribe("llm:_:complete", judge)
            await client.connect()
            for _ in range(100):
                try:
                    await client.request("loop:_:list", {})
                    break
                except Exception:
                    await asyncio.sleep(.1)
            await client.request("config:_:set", {"plugin": "plugins.viewer-chat", "key": "hindsight", "value": {"retain_enabled": False}})
            await client.request("chat:_:routing:put", {"default_routing_policy_id": "test", "routing_policies": [{"id": "test", "name": "mock", "enabled": True, "auto_failover": False, "candidates": [{"id": "mock", "agent_id": "hermes", "provider_id": "default", "enabled": True, "parameters": {"profile": "mock-profile"}}]}]})
            role = await client.request("chat:_:roles:create", {"name": "Explicit worker", "description": "test", "routing_policy_id": "test"})
            chat = await client.request("chat:_:chats:create", {"name": "Loop smoke", "root": str(root), "member_role_ids": [role["id"]]})
            created = await client.request("loop:_:create", {"id": "smoke-loop", "chat_id": chat["id"], "role_id": role["id"], "goal": "two steps", "criteria": "mock evidence", "max_iterations": 5, "duration_seconds": 120, "turn_timeout_seconds": 30})
            assert created["branch_id"] and created["state"] == "draft"
            await client.request("loop:_:start", {"id": created["id"]})
            detail = await until_state(created["id"], {"completed", "paused", "limited"})
            assert detail["loop"]["state"] == "completed", detail
            assert detail["loop"]["iteration"] == 2 and verdicts == 2, detail
            ids = [i["turn_id"] for i in detail["iterations"]]
            assert len(set(ids)) == 2 and all(ids), ids
            with sqlite3.connect(root / "data/chat.sqlite3") as db:
                assert db.execute("select count(*) from dispatch_receipts").fetchone()[0] == 2
                assert db.execute("select count(*) from turns where branch_id = ?", (created["branch_id"],)).fetchone()[0] == 2
            print("PASS loop uses chat turns, judges completion, persists iteration references")
            # An explicit one-iteration cap ends as limited even when judge
            # recommends continuing. Finished branches remain available.
            verdicts = 0
            limited = await client.request("loop:_:create", {"chat_id": chat["id"], "role_id": role["id"], "goal": "bounded", "criteria": "not yet done", "max_iterations": 1})
            await client.request("loop:_:start", {"id": limited["id"]})
            result = await until_state(limited["id"], {"limited", "paused", "completed"})
            assert result["loop"]["state"] == "limited", result
            print("PASS iteration cap is not reported as goal completion")
        except Exception:
            print((root / "server.log").read_text(errors="replace")[-6000:], file=sys.stderr)
            raise
        finally:
            await client.close()
            process.terminate()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill(); process.wait(timeout=5)

if __name__ == "__main__":
    asyncio.run(run())
