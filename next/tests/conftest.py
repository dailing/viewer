from __future__ import annotations

import asyncio
import json
import uuid
from collections.abc import AsyncIterator
from typing import Any

import pytest
from websockets.asyncio.client import ClientConnection, connect

from kernel.server import KernelServer, ServerConfig


def hello(plugin: str, *, conn: str | None = None, instance: str | None = None) -> dict[str, Any]:
    frame: dict[str, Any] = {
        "type": "hello",
        "protocol_version": 1,
        "conn": conn or str(uuid.uuid4()),
        "manifest": {"id": plugin, "version": "1.0.0", "slots": {}, "emits": {}},
        "managed": False,
    }
    if instance is not None:
        frame["instance_id"] = instance
    return frame


async def open_client(
    server: KernelServer, plugin: str, *, instance: str | None = None
) -> tuple[ClientConnection, dict[str, Any]]:
    websocket = await connect(f"ws://127.0.0.1:{server.port}/ws")
    greeting = hello(plugin, instance=instance)
    await websocket.send(json.dumps(greeting))
    return websocket, greeting


async def receive_channel(
    websocket: ClientConnection, channel: str, timeout: float = 2
) -> dict[str, Any]:
    async with asyncio.timeout(timeout):
        while True:
            frame = json.loads(await websocket.recv())
            if frame.get("channel") == channel:
                return frame


@pytest.fixture
async def kernel() -> AsyncIterator[KernelServer]:
    server = KernelServer(ServerConfig(port=0))
    await server.start()
    try:
        yield server
    finally:
        await server.stop()
