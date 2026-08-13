#!/usr/bin/env python3
"""Black-box M6b workspace/chat/dual-role relay smoke through the Go bus."""
from __future__ import annotations
import asyncio, os, socket, sqlite3, subprocess, sys, tempfile, time
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
            process = subprocess.Popen([str(viewerd), "--plugins=config-store,chat", "--kernel-port", str(port), "--data-dir", str(data_dir)], env=environment, stdout=log, stderr=subprocess.STDOUT)
        client = BusClient(f"ws://127.0.0.1:{port}/ws", CALLER, request_timeout=25.0)
        messages: list[dict[str, Any]] = []; completions: list[dict[str, Any]] = []; active: list[Any] = []
        try:
            await wait_port(port)
            registry: list[list[dict[str, Any]]] = []
            async def collect_messages(frame: dict[str, Any]) -> None: messages.append(frame["value"])
            async def collect_completions(frame: dict[str, Any]) -> None: completions.append(frame["value"])
            async def collect_active(frame: dict[str, Any]) -> None: active.append(frame["value"])
            async def collect_registry(frame: dict[str, Any]) -> None: registry.append(frame["value"])
            await client.subscribe("chat:*:message", collect_messages)
            await client.subscribe("chat:*:turn-completed", collect_completions)
            await client.subscribe("chat:_:active", collect_active)
            await client.subscribe("plugins:_:list", collect_registry)
            await client.connect()
            await wait_for(lambda: registry and {item["manifest"]["id"] for item in registry[-1]} >= {"chat", "config-store"})
            first = await client.request("chat:_:roles:create", {"name": "Planner", "description": "plans", "prompt": "PLAN-RULE", "provider": "hermes"})
            second = await client.request("chat:_:roles:create", {"name": "Builder", "description": "builds", "prompt": "BUILD-RULE", "provider": "hermes"})
            await client.request("chat:_:workspace:patch", {"common_prompt": "COMMON-RULE"})
            chat = await client.request("chat:_:chats:create", {"name": "Relay", "root": str(ROOT), "type": "group", "member_role_ids": [first["id"], second["id"]], "common_prompt": "CHAT-RULE"})
            activated = await client.request("chat:_:chats:activate", {"id": chat["id"]})
            assert activated["id"] == chat["id"]
            await wait_for(lambda: active and active[-1] == chat["id"])
            print("PASS workspace roles and chat CRUD + active mailbox")

            dispatched = await client.request("chat:_:dispatch", {"chat_id": chat["id"], "message": "relay hello", "role_ids": [first["id"], second["id"]]})
            assert dispatched["role_ids"] == [first["id"], second["id"]]
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
            finally: database.close()
            print("PASS explicit dual-role ordered relay, sender frames, prompts, and DB rows")

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
