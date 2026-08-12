"""C4 http-gateway tests: real kernel, real gateway, raw browser connections."""

from __future__ import annotations

import asyncio
import json
import urllib.request
from pathlib import Path

import pytest
import websockets
from websockets.exceptions import ConnectionClosed

from kernel.server import KernelServer
from plugins.gateway import HttpGatewayPlugin
from sdk import BusClient

BROWSER_1 = "11111111-1111-4111-8111-111111111111"
BROWSER_9 = "99999999-9999-4999-8999-999999999999"


def url(port: int) -> str:
    return f"ws://127.0.0.1:{port}/ws"


PRODUCER = {"id": "producer", "version": "0", "slots": {}, "emits": {}}


@pytest.fixture
async def gateway(kernel: KernelServer, tmp_path: Path):
    static = tmp_path / "static"
    static.mkdir()
    (static / "index.html").write_text("<html>viewer next</html>")
    plugin = HttpGatewayPlugin(port=0, static_dir=static)
    await plugin.start(url(kernel.port))
    try:
        yield plugin
    finally:
        await plugin.stop()


async def browser_connect(gateway_port: int, conn: str = BROWSER_1):
    ws = await websockets.connect(f"ws://127.0.0.1:{gateway_port}/ws")
    await ws.send(json.dumps({"type": "hello", "protocol_version": 1, "conn": conn}))
    return ws


@pytest.mark.asyncio
async def test_browser_subscribe_live_and_retained_replay(
    kernel: KernelServer, gateway: HttpGatewayPlugin
) -> None:
    producer = BusClient(url(kernel.port), PRODUCER)
    await producer.connect()
    await producer.set("demo:_:state", {"v": "retained"})

    browser = await browser_connect(gateway.bound_port)
    await browser.send(json.dumps({"type": "subscribe", "pattern": "demo:_:state"}))
    # Per-browser kernel connection => the kernel replays the mailbox value.
    retained = json.loads(await asyncio.wait_for(browser.recv(), 2))
    assert retained["channel"] == "demo:_:state"
    assert retained["value"] == {"v": "retained"}
    assert retained["type"] == "set"

    await producer.publish("demo:_:state", {"v": "live"})
    live = json.loads(await asyncio.wait_for(browser.recv(), 2))
    assert live["type"] == "publish"
    assert live["value"] == {"v": "live"}
    await browser.close()
    await producer.close()


@pytest.mark.asyncio
async def test_browser_publish_gets_gateway_origin(
    kernel: KernelServer, gateway: HttpGatewayPlugin
) -> None:
    producer = BusClient(url(kernel.port), PRODUCER)
    seen: dict = {}
    got = asyncio.Event()

    async def handler(frame: dict) -> None:
        seen.update(frame)
        got.set()

    await producer.subscribe("input:_:event", handler)
    await producer.connect()

    browser = await browser_connect(gateway.bound_port, conn=BROWSER_9)
    await browser.send(json.dumps({"type": "publish", "channel": "input:_:event", "value": 1}))
    await asyncio.wait_for(got.wait(), 2)
    assert seen["origin"]["plugin"] == "gateway"
    assert seen["origin"]["instance"] == BROWSER_9
    await browser.close()
    await producer.close()


@pytest.mark.asyncio
async def test_browser_bad_hello_rejected(gateway: HttpGatewayPlugin) -> None:
    ws = await websockets.connect(f"ws://127.0.0.1:{gateway.bound_port}/ws")
    await ws.send(json.dumps({"type": "publish", "channel": "x:_:y", "value": 1}))
    with pytest.raises(ConnectionClosed) as closed:
        await ws.recv()
    assert closed.value.code == 4002
    await ws.close()


@pytest.mark.asyncio
async def test_static_assets_served(gateway: HttpGatewayPlugin) -> None:
    def fetch() -> str:
        with urllib.request.urlopen(
            f"http://127.0.0.1:{gateway.bound_port}/", timeout=2
        ) as response:
            return response.read().decode()

    body = await asyncio.to_thread(fetch)
    assert body == "<html>viewer next</html>"
