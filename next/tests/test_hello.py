from __future__ import annotations

import asyncio
import json
import uuid

import pytest
from websockets.asyncio.client import connect
from websockets.exceptions import ConnectionClosed

from conftest import hello, open_client, receive_channel
from kernel.server import KernelServer


@pytest.mark.asyncio
async def test_registry_lifecycle_disconnect_and_stamping(kernel: KernelServer) -> None:
    observer, _ = await open_client(kernel, "observer")
    await observer.send(json.dumps({"type": "subscribe", "pattern": "plugins"}))
    plugin, greeting = await open_client(kernel, "worker", instance="job-1")
    while True:
        listing = await receive_channel(observer, "plugins:_:list")
        if any(entry["conn"] == greeting["conn"] for entry in listing["value"]):
            break
    activated = await receive_channel(observer, "plugins:worker:lifecycle")
    assert activated["value"]["state"] == "activated"

    await observer.send(json.dumps({"type": "subscribe", "pattern": "stamp"}))
    await plugin.send(json.dumps({
        "type": "publish", "channel": "stamp:_:event", "value": None,
        "ts": 1, "origin": {"plugin": "forged", "instance": "forged"}
    }))
    stamped = await receive_channel(observer, "stamp:_:event")
    assert stamped["ts"] > 1
    assert stamped["origin"] == {"plugin": "worker", "instance": "job-1"}

    await plugin.close()
    while True:
        listing = await receive_channel(observer, "plugins:_:list")
        if not any(entry["conn"] == greeting["conn"] for entry in listing["value"]):
            break
    deactivated = await receive_channel(observer, "plugins:worker:lifecycle")
    assert deactivated["value"]["state"] == "deactivated"
    await observer.close()


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("frame", "code"),
    [
        ({"type": "subscribe", "pattern": ">"}, 4001),
        ({"type": "hello"}, 4002),
        (hello("wrong") | {"protocol_version": 2}, 4003),
    ],
)
async def test_fatal_hello_errors(kernel: KernelServer, frame: dict, code: int) -> None:
    websocket = await connect(f"ws://127.0.0.1:{kernel.port}/ws")
    await websocket.send(json.dumps(frame))
    with pytest.raises(ConnectionClosed) as raised:
        await websocket.recv()
    assert raised.value.code == code
