#!/usr/bin/env python3
"""Black-box milestone-1 smoke tests for the Go Viewer kernel."""

from __future__ import annotations

import asyncio
import json
import os
import signal
import socket
import subprocess
import sys
import tempfile
import uuid
from pathlib import Path
from typing import Any, Awaitable, Callable

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))

import websockets  # noqa: E402
from websockets.exceptions import ConnectionClosed  # noqa: E402

from sdk import BusClient  # noqa: E402

HOST = os.environ.get("VIEWER_KERNEL_HOST", "127.0.0.1")
PORT = int(os.environ.get("VIEWER_KERNEL_PORT", "28766"))
URL = f"ws://{HOST}:{PORT}/ws"


def manifest(plugin_id: str) -> dict[str, Any]:
    return {"id": plugin_id, "version": "1.0.0", "slots": {}, "emits": {}}


def hello(plugin_id: str, *, instance_id: str | None = None) -> dict[str, Any]:
    frame: dict[str, Any] = {
        "type": "hello",
        "protocol_version": 1,
        "conn": str(uuid.uuid4()),
        "manifest": manifest(plugin_id),
        "managed": False,
    }
    if instance_id is not None:
        frame["instance_id"] = instance_id
    return frame


async def open_raw(
    plugin_id: str, *, url: str = URL, instance_id: str | None = None
) -> tuple[Any, dict[str, Any]]:
    websocket = await websockets.connect(url)
    greeting = hello(plugin_id, instance_id=instance_id)
    await websocket.send(json.dumps(greeting, separators=(",", ":")))
    return websocket, greeting


async def receive_channel(websocket: Any, channel: str, timeout: float = 3.0) -> dict[str, Any]:
    async with asyncio.timeout(timeout):
        while True:
            frame = json.loads(await websocket.recv())
            if frame.get("channel") == channel:
                return frame


async def close_all(*clients: Any) -> None:
    await asyncio.gather(*(client.close() for client in clients if client is not None), return_exceptions=True)


async def test_hello_success() -> None:
    ws, greeting = await open_raw("hello-ok")
    try:
        await ws.send(json.dumps({"type": "subscribe", "pattern": "plugins:_:list"}))
        listing = await receive_channel(ws, "plugins:_:list")
        assert any(entry["conn"] == greeting["conn"] for entry in listing["value"])
    finally:
        await ws.close()


async def expect_close(first_frame: dict[str, Any], code: int) -> None:
    ws = await websockets.connect(URL)
    try:
        await ws.send(json.dumps(first_frame))
        try:
            await ws.recv()
        except ConnectionClosed as exc:
            assert exc.code == code, (exc.code, exc.reason)
        else:
            raise AssertionError(f"connection stayed open; expected close {code}")
    finally:
        await ws.close()


async def test_hello_4001() -> None:
    await expect_close({"type": "subscribe", "pattern": ">"}, 4001)


async def test_hello_4002() -> None:
    await expect_close(
        {
            "type": "hello",
            "protocol_version": 1,
            "conn": str(uuid.uuid4()),
            "managed": False,
        },
        4002,
    )


async def test_hello_4003() -> None:
    greeting = hello("wrong-version")
    greeting["protocol_version"] = 2
    await expect_close(greeting, 4003)


async def test_publish_fanout() -> None:
    wildcard = BusClient(URL, manifest("wildcard"), reconnect=False)
    catch_all = BusClient(URL, manifest("catch-all"), reconnect=False)
    producer = BusClient(URL, manifest("producer"), instance_id="job-7", reconnect=False)
    wildcard_frame: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()
    all_frame: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()

    async def save_wildcard(frame: dict[str, Any]) -> None:
        if frame["channel"] == "fan:42:event" and not wildcard_frame.done():
            wildcard_frame.set_result(frame)

    async def save_all(frame: dict[str, Any]) -> None:
        if frame["channel"] == "fan:42:event" and not all_frame.done():
            all_frame.set_result(frame)

    await wildcard.subscribe("fan:*:event", save_wildcard)
    await catch_all.subscribe(">", save_all)
    try:
        await asyncio.gather(wildcard.connect(), catch_all.connect(), producer.connect())
        await asyncio.gather(wildcard.wait_registered(), catch_all.wait_registered(), producer.wait_registered())
        await asyncio.sleep(0.05)
        await producer.publish(
            "fan:42:event",
            {"ok": True},
        )
        first, second = await asyncio.gather(
            asyncio.wait_for(wildcard_frame, 3), asyncio.wait_for(all_frame, 3)
        )
        for frame in (first, second):
            assert frame["value"] == {"ok": True}
            assert frame["origin"] == {"plugin": "producer", "instance": "job-7"}
            assert frame["ts"] > 1_000_000_000_000
    finally:
        await close_all(wildcard, catch_all, producer)


async def test_mailbox_atomic_handoff() -> None:
    producer, _ = await open_raw("mailbox-producer")
    subscriber = None
    try:
        await producer.send(json.dumps({"type": "set", "channel": "race:_:state", "value": 0}))
        await producer.send(json.dumps({"type": "subscribe", "pattern": "race:_:state"}))
        assert (await receive_channel(producer, "race:_:state"))["value"] == 0

        subscriber, _ = await open_raw("mailbox-subscriber")
        await asyncio.gather(
            subscriber.send(json.dumps({"type": "subscribe", "pattern": "race:_:state"})),
            producer.send(json.dumps({"type": "set", "channel": "race:_:state", "value": 1})),
        )
        first = await receive_channel(subscriber, "race:_:state")
        values = [first["value"]]
        if values == [0]:
            values.append((await receive_channel(subscriber, "race:_:state"))["value"])
        assert values in ([1], [0, 1]), values
        try:
            duplicate = await receive_channel(subscriber, "race:_:state", timeout=0.15)
        except TimeoutError:
            pass
        else:
            raise AssertionError(f"duplicate mailbox delivery: {duplicate}")
    finally:
        await close_all(producer, subscriber)


async def test_rpc_inbox_roundtrip() -> None:
    caller = BusClient(URL, manifest("rpc-caller"), reconnect=False)
    callee = BusClient(URL, manifest("rpc-callee"), reconnect=False)

    async def respond(frame: dict[str, Any]) -> None:
        value = frame["value"]
        await callee.publish(
            value["_reply_to"],
            {"_corr": value["_corr"], "ok": True, "result": {"echo": value["message"]}},
        )

    await callee.subscribe("echo:_:request", respond)
    try:
        await asyncio.gather(caller.connect(), callee.connect())
        await asyncio.gather(caller.wait_registered(), callee.wait_registered())
        await asyncio.sleep(0.05)
        result = await caller.request("echo:_:request", {"message": "roundtrip"}, timeout=3)
        assert result == {"echo": "roundtrip"}
    finally:
        await close_all(caller, callee)


async def test_oversize_error_mailbox() -> None:
    client = BusClient(URL, manifest("oversize"), reconnect=False)
    seen = asyncio.Event()
    values: list[dict[str, Any]] = []

    def on_error(value: dict[str, Any]) -> None:
        values.append(value)
        if value.get("code") == "frame_too_large":
            seen.set()

    client.on_error(on_error)
    try:
        await client.connect()
        await client.wait_registered()
        await client.publish("oversize:_:event", "x" * (1024 * 1024 + 1))
        await asyncio.wait_for(seen.wait(), 5)
        value = next(item for item in values if item.get("code") == "frame_too_large")
        assert value["detail"]["size"] > 1024 * 1024
        assert value["detail"]["limit"] == 1024 * 1024
        assert client.connected
    finally:
        await client.close()


async def test_registry_and_lifecycle() -> None:
    observer, _ = await open_raw("registry-observer")
    worker = None
    try:
        await observer.send(json.dumps({"type": "subscribe", "pattern": "plugins"}))
        await receive_channel(observer, "plugins:_:list")
        worker, greeting = await open_raw("registry-worker", instance_id="instance-9")
        while True:
            listing = await receive_channel(observer, "plugins:_:list")
            match = next((entry for entry in listing["value"] if entry["conn"] == greeting["conn"]), None)
            if match is not None:
                break
        assert match["id"] == "registry-worker"
        assert match["instance_id"] == "instance-9"
        assert match["manifest"] == greeting["manifest"]
        activated = await receive_channel(observer, "plugins:registry-worker:lifecycle")
        assert activated["value"] == {"state": "activated", "conn": greeting["conn"]}
        await worker.close()
        worker = None
        while True:
            listing = await receive_channel(observer, "plugins:_:list")
            if not any(entry["conn"] == greeting["conn"] for entry in listing["value"]):
                break
        deactivated = await receive_channel(observer, "plugins:registry-worker:lifecycle")
        assert deactivated["value"] == {"state": "deactivated", "conn": greeting["conn"]}
    finally:
        await close_all(observer, worker)


def unused_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


async def test_sigterm_close_4009() -> None:
    binary = Path(os.environ.get("VIEWERD_BIN", "/tmp/viewerd"))
    if not binary.is_file():
        raise AssertionError(f"viewerd binary not found: {binary}")
    port = unused_port()
    data_dir = tempfile.mkdtemp(prefix="viewer-kernel-4009-")
    process = subprocess.Popen(
        [str(binary), "--plugins=none", "--kernel-port", str(port), "--data-dir", data_dir],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        text=True,
    )
    ws = None
    try:
        child_url = f"ws://127.0.0.1:{port}/ws"
        for _ in range(50):
            if process.poll() is not None:
                stderr = (process.stderr.read() if process.stderr else "")[-4000:]
                raise AssertionError(f"child kernel exited early ({process.returncode}): {stderr}")
            try:
                ws, _ = await open_raw("shutdown-client", url=child_url)
                break
            except OSError:
                await asyncio.sleep(0.05)
        if ws is None:
            raise AssertionError("child kernel did not start")
        process.send_signal(signal.SIGTERM)
        try:
            await asyncio.wait_for(ws.recv(), 5)
        except ConnectionClosed as exc:
            assert exc.code == 4009, (exc.code, exc.reason)
        else:
            raise AssertionError("client did not receive shutdown close")
        return_code = await asyncio.to_thread(process.wait, 5)
        assert return_code == 0, return_code
    finally:
        if ws is not None:
            await ws.close()
        if process.poll() is None:
            process.kill()
            process.wait(timeout=5)


TESTS: list[tuple[str, Callable[[], Awaitable[None]]]] = [
    ("hello success", test_hello_success),
    ("first frame close 4001", test_hello_4001),
    ("invalid hello close 4002", test_hello_4002),
    ("protocol mismatch close 4003", test_hello_4003),
    ("publish fanout wildcard and >", test_publish_fanout),
    ("mailbox atomic handoff", test_mailbox_atomic_handoff),
    ("RPC inbox roundtrip", test_rpc_inbox_roundtrip),
    ("1 MiB frame rejection", test_oversize_error_mailbox),
    ("registry and lifecycle", test_registry_and_lifecycle),
    ("SIGTERM close 4009", test_sigterm_close_4009),
]


async def main() -> int:
    failures = 0
    for name, test in TESTS:
        try:
            await test()
        except Exception as exc:
            failures += 1
            print(f"FAIL {name}: {type(exc).__name__}: {exc}", flush=True)
        else:
            print(f"PASS {name}", flush=True)
    print(f"RESULT {len(TESTS) - failures}/{len(TESTS)} passed", flush=True)
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
