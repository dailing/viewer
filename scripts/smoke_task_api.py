#!/usr/bin/env python3
"""Task-first public API, idempotency, session and history black-box smoke."""
from __future__ import annotations
import asyncio, json, os, socket, sqlite3, subprocess, sys, tempfile, time
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any, AsyncIterator, Callable
ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from sdk import BusClient  # noqa: E402
CALLER = {"id": "task-api-smoke", "version": "1.0.0", "slots": {}, "emits": {}}
def free_port() -> int:
    with socket.socket() as sock: sock.bind(("127.0.0.1", 0)); return int(sock.getsockname()[1])
async def wait_for(probe: Callable[[], Any], timeout: float = 20.0) -> Any:
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        if value := probe(): return value
        await asyncio.sleep(0.03)
    raise TimeoutError("Task smoke condition timed out")
async def wait_port(port: int) -> None:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            _, writer = await asyncio.open_connection("127.0.0.1", port); writer.close(); await writer.wait_closed(); return
        except OSError: await asyncio.sleep(0.05)
    raise TimeoutError(f"kernel port {port} did not open")
@asynccontextmanager
async def chat_server(manifest: dict[str, Any] = CALLER) -> AsyncIterator[tuple[BusClient, Path, list[dict[str, Any]]]]:
    viewerd, mock, port = Path(os.environ["VIEWERD_BIN"]), Path(__file__).with_name("mock_acp_agent.py"), free_port()
    with tempfile.TemporaryDirectory(prefix="viewer-task-api-smoke-") as temp:
        data_dir, log_path = Path(temp) / "data", Path(temp) / "viewerd.log"
        environment = {**os.environ, "VIEWER_HERMES_COMMAND": str(mock), "VIEWER_HERMES_PROFILE": "mock-profile", "VIEWER_HERMES_YOLO": "true"}
        with log_path.open("wb") as log:
            process = subprocess.Popen([str(viewerd), "--plugins=config-store,llm,viewer.agent-hermes,chat", "--kernel-port", str(port), "--data-dir", str(data_dir)], env=environment, stdout=log, stderr=subprocess.STDOUT)
        client = BusClient(f"ws://127.0.0.1:{port}/ws", manifest, request_timeout=25.0)
        completions: list[dict[str, Any]] = []; registry: list[Any] = []
        async def completed(frame: dict[str, Any]) -> None: completions.append(frame["value"])
        async def registered(frame: dict[str, Any]) -> None: registry.append(frame["value"])
        try:
            await wait_port(port)
            await client.subscribe("chat:*:turn-completed", completed); await client.subscribe("plugins:_:list", registered)
            await client.connect()
            await wait_for(lambda: registry and {item["manifest"]["id"] for item in registry[-1]} >= {"chat", "viewer.agent-hermes"})
            yield client, data_dir, completions
        except Exception:
            if log_path.exists(): print(log_path.read_text(errors="replace")[-4000:], file=sys.stderr)
            raise
        finally:
            await client.close(); process.terminate()
            try: process.wait(timeout=10)
            except subprocess.TimeoutExpired: process.kill(); process.wait(timeout=5)
async def make_workspace(client: BusClient) -> tuple[dict[str, Any], dict[str, Any], dict[str, Any]]:
    routing = {"default_routing_policy_id": "hermes-policy", "routing_policies": [{"id": "hermes-policy", "name": "Hermes", "enabled": True, "auto_failover": False, "max_attempts": 1, "candidates": [{"id": "hermes-default", "name": "Hermes default", "agent_id": "hermes", "provider_id": "default", "model_id": "", "enabled": True, "parameters": {"profile": "mock-profile"}}]}]}
    await client.request("chat:_:routing:put", routing)
    role = await client.request("chat:_:roles:create", {"name": "Task Role", "prompt": "TASK-ROLE", "routing_policy_id": "hermes-policy"})
    chat = await client.request("chat:_:chats:create", {"name": "Task API", "root": str(ROOT), "type": "project", "member_role_ids": [role["id"]]})
    tasks = await client.request("chat:_:tasks:list", {"chat_id": chat["id"], "status": "open", "limit": 100})
    return role, chat, tasks["tasks"][0]
async def dispatch(client: BusClient, task_id: str, text: str, role_id: str, key: str) -> dict[str, Any]:
    return await client.request("chat:_:send-message", {"task_id": task_id, "role_ids": [role_id], "text": text, "idempotency_key": key})
async def run() -> None:
    async with chat_server() as (client, data_dir, completions):
        role, chat, default = await make_workspace(client)
        created = await client.request("chat:_:tasks:create", {"chat_id": chat["id"], "title": "same title", "idempotency_key": "create-shared"})
        retry = await client.request("chat:_:tasks:create", {"chat_id": chat["id"], "title": "same title", "idempotency_key": "create-shared"})
        assert created["task"]["id"] == retry["task"]["id"]
        try: await client.request("chat:_:tasks:create", {"chat_id": chat["id"], "title": "different request", "idempotency_key": "create-shared"})
        except Exception as exc: assert "different request" in str(exc)
        else: raise AssertionError("idempotency key accepted a conflicting request")
        task_a = created["task"]
        other = await client.request("chat:_:tasks:create", {"chat_id": chat["id"], "title": "same title", "idempotency_key": "create-other"})
        task_b = other["task"]
        assert task_a["id"] != task_b["id"] and task_a["title"] == task_b["title"]
        first = await dispatch(client, task_a["id"], "once", role["id"], "dispatch-once")
        duplicate = await dispatch(client, task_a["id"], "once", role["id"], "dispatch-once")
        assert first["dispatch_id"] == duplicate["dispatch_id"]
        await wait_for(lambda: any(item["task_id"] == task_a["id"] for item in completions))
        await dispatch(client, task_b["id"], "independent", role["id"], "dispatch-other")
        await wait_for(lambda: any(item["task_id"] == task_b["id"] for item in completions))
        database = sqlite3.connect(data_dir / "chat.sqlite3")
        try:
            assert database.execute("select count(*) from messages where task_id=? and role='user' and text='once'", (task_a["id"],)).fetchone()[0] == 1
            sessions = database.execute("select task_id,provider_session_id from role_sessions where role_id=? order by task_id", (role["id"],)).fetchall()
            assert len(sessions) == 2 and sessions[0][0] != sessions[1][0] and sessions[0][1] != sessions[1][1]
        finally: database.close()
        # A second caller may reuse the same key without colliding with the first caller.
        other_caller = BusClient(client.url, {"id": "other-task-caller", "version": "1", "slots": {}, "emits": {}}, request_timeout=25.0)
        await other_caller.connect()
        try:
            separate = await other_caller.request("chat:_:tasks:create", {"chat_id": chat["id"], "title": "other caller", "idempotency_key": "create-shared"})
            assert separate["task"]["id"] != task_a["id"]
        finally: await other_caller.close()
        for index in range(52):
            await client.request("chat:_:tasks:create", {"chat_id": chat["id"], "title": f"page {index}", "idempotency_key": f"page-{index}"})
        page1 = await client.request("chat:_:tasks:list", {"chat_id": chat["id"], "status": "open", "limit": 50})
        assert len(page1["tasks"]) == 50 and page1["has_more"]
        page2 = await client.request("chat:_:tasks:list", {"chat_id": chat["id"], "status": "open", "limit": 50, "before_updated_at": page1["next_updated_at"], "before_id": page1["next_id"]})
        assert page2["tasks"] and not ({item["id"] for item in page1["tasks"]} & {item["id"] for item in page2["tasks"]})
        # Around restores the anchor and both directions without loading the whole Task.
        history = await client.request("chat:_:history", {"task_id": task_a["id"], "include_messages": True, "limit": 50})
        anchor = history["messages"][0]
        around = await client.request("chat:_:history", {"task_id": task_a["id"], "include_messages": True, "limit": 1, "around": anchor["created_at"], "message_id": anchor["id"]})
        assert any(item["id"] == anchor["id"] for item in around["messages"])
        await client.request("chat:_:tasks:pin", {"task_id": task_a["id"], "pinned": True})
        ended = await client.request("chat:_:tasks:end", {"task_id": task_a["id"]})
        assert ended["task"]["status"] == "ended" and not ended["task"]["pinned"]
        try: await dispatch(client, task_a["id"], "read only", role["id"], "ended-write")
        except Exception as exc: assert "ended" in str(exc).lower()
        else: raise AssertionError("ended Task accepted a dispatch without explicit continuation")
        continued = await client.request("chat:_:send-message", {"task_id": task_a["id"], "role_ids": [role["id"]], "text": "continue", "idempotency_key": "ended-continue", "reopen_ended": True})
        assert continued["task_id"] == task_a["id"]
        reopened = await client.request("chat:_:tasks:get", {"task_id": task_a["id"]})
        assert reopened["status"] == "open"
        await wait_for(lambda: sum(item["task_id"] == task_a["id"] for item in completions) >= 2)
        print("PASS Task create/dispatch idempotency, caller isolation, duplicate titles, sessions, pagination, around, ended boundary and explicit continuation")
def main() -> None: asyncio.run(run()); print("PASS task API smoke complete")
if __name__ == "__main__": main()
