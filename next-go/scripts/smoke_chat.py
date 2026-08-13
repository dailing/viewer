#!/usr/bin/env python3
"""Black-box M6a chat smoke through the Go bus and mock ACP agent."""

from __future__ import annotations

import asyncio
import os
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Any, Callable


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(os.environ.get("VIEWER_SMOKE_NEXT", ROOT / "next"))))

from sdk import BusClient  # noqa: E402


CALLER = {"id": "chat-smoke", "version": "0.1.0", "slots": {}, "emits": {}}


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


async def wait_for(probe: Callable[[], Any], timeout: float = 15.0) -> Any:
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        value = probe()
        if value:
            return value
        await asyncio.sleep(0.03)
    raise TimeoutError("chat smoke condition timed out")


async def wait_port(port: int) -> None:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            _, writer = await asyncio.open_connection("127.0.0.1", port)
            writer.close()
            await writer.wait_closed()
            return
        except OSError:
            await asyncio.sleep(0.05)
    raise TimeoutError(f"kernel port {port} did not open")


async def run() -> None:
    viewerd = Path(os.environ["VIEWERD_BIN"])
    mock = Path(__file__).with_name("mock_acp_agent.py")
    port = free_port()
    with tempfile.TemporaryDirectory(prefix="viewer-chat-smoke-") as temp:
        data_dir = Path(temp) / "data"
        log_path = Path(temp) / "viewerd.log"
        environment = os.environ.copy()
        environment.update(
            {
                "VIEWER_HERMES_COMMAND": str(mock),
                "VIEWER_HERMES_PROFILE": "mock-profile",
                "VIEWER_HERMES_YOLO": "true",
            }
        )
        with log_path.open("wb") as log:
            process = subprocess.Popen(
                [
                    str(viewerd),
                    "--plugins=chat",
                    "--kernel-port",
                    str(port),
                    "--data-dir",
                    str(data_dir),
                ],
                env=environment,
                stdout=log,
                stderr=subprocess.STDOUT,
            )
        client = BusClient(f"ws://127.0.0.1:{port}/ws", CALLER, request_timeout=20.0)
        messages: list[dict[str, Any]] = []
        completions: list[dict[str, Any]] = []
        registries: list[list[dict[str, Any]]] = []

        async def on_message(frame: dict[str, Any]) -> None:
            messages.append(frame["value"])

        async def on_completed(frame: dict[str, Any]) -> None:
            completions.append(frame["value"])

        async def on_registry(frame: dict[str, Any]) -> None:
            registries.append(frame["value"])

        try:
            await wait_port(port)
            await client.subscribe("chat:*:message", on_message)
            await client.subscribe("chat:*:turn-completed", on_completed)
            await client.subscribe("plugins:_:list", on_registry)
            await client.connect()
            await wait_for(
                lambda: registries
                and any(item["manifest"]["id"] == "chat" for item in registries[-1])
            )

            first = await client.request(
                "chat:_:send-message",
                {"chat_id": "smoke-normal", "text": "hello", "cwd": str(ROOT)},
            )
            assert first["accepted"] is True and first["turn_id"]
            completed = await wait_for(
                lambda: next(
                    (item for item in completions if item["turn_id"] == first["turn_id"]),
                    None,
                )
            )
            assert completed["stop_reason"] == "end_turn"
            turn_messages = [item for item in messages if item["turn_id"] == first["turn_id"]]
            assert [item["role"] for item in turn_messages] == ["user", "assistant", "assistant"]
            assert "".join(item["text"] for item in turn_messages[1:]) == "mock: hello"

            database = sqlite3.connect(data_dir / "chat.sqlite3")
            try:
                assert database.execute("select count(*) from chats").fetchone()[0] == 1
                assert database.execute("select count(*) from messages").fetchone()[0] == 3
                assert database.execute("select count(*) from turns").fetchone()[0] == 1
                row = database.execute(
                    "select chat_id, stop_reason, ended_at from turns where id = ?", (first["turn_id"],)
                ).fetchone()
                assert row and row[0] == "smoke-normal" and row[1] == "end_turn" and row[2]
                session_before = database.execute(
                    "select provider_session_id from chats where id = 'smoke-normal'"
                ).fetchone()[0]
            finally:
                database.close()
            print("PASS streamed turn events and chats/messages/turns persistence")

            second = await client.request(
                "chat:_:send-message", {"chat_id": "smoke-normal", "text": "again"}
            )
            await wait_for(lambda: any(item["turn_id"] == second["turn_id"] for item in completions))
            database = sqlite3.connect(data_dir / "chat.sqlite3")
            try:
                session_after = database.execute(
                    "select provider_session_id from chats where id = 'smoke-normal'"
                ).fetchone()[0]
            finally:
                database.close()
            assert session_after == session_before
            print("PASS same-chat provider session reuse")

            long_turn = await client.request(
                "chat:_:send-message",
                {"chat_id": "smoke-cancel", "text": "long turn", "cwd": str(ROOT)},
            )
            await wait_for(
                lambda: any(item["turn_id"] == long_turn["turn_id"] and item["role"] == "assistant" for item in messages)
            )
            stopped = await client.request("chat:_:stop", {"chat_id": "smoke-cancel"})
            assert stopped == {"stopped": True}
            cancelled = await wait_for(
                lambda: next(
                    (item for item in completions if item["turn_id"] == long_turn["turn_id"]),
                    None,
                )
            )
            assert cancelled["stop_reason"] == "cancelled"
            assert await client.request("chat:_:stop", {"chat_id": "smoke-cancel"}) == {"stopped": False}
            print("PASS long-turn session/cancel and idempotent stop")
        except Exception:
            if log_path.exists():
                print(log_path.read_text(errors="replace")[-4000:], file=sys.stderr)
            raise
        finally:
            await client.close()
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
            if process.returncode not in {0, -15}:
                excerpt = log_path.read_text(errors="replace")[-4000:]
                raise RuntimeError(f"viewerd exited {process.returncode}:\n{excerpt}")


def main() -> None:
    asyncio.run(run())
    print("PASS chat smoke complete")


if __name__ == "__main__":
    main()
