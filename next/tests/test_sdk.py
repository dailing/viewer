"""SDK tests against a real kernel (no mocks — the kernel is the spec)."""

from __future__ import annotations

import asyncio
import socket

import pytest

from conftest import open_client
from kernel.server import KernelServer, ServerConfig
from sdk import BusClient, Plugin, RpcError, slot

import json

ECHO_MANIFEST = {"id": "echo", "version": "0.1.0", "slots": {"get": {}}, "emits": {}}
SECOND_MANIFEST = {"id": "second", "version": "0.1.0", "slots": {}, "emits": {}}


def url(port: int) -> str:
    return f"ws://127.0.0.1:{port}/ws"


class EchoPlugin(Plugin):
    manifest = ECHO_MANIFEST

    @slot("echo:_:get")
    async def get(self, ctx) -> None:
        await ctx.respond({"echo": ctx.value["key"]})

    @slot("echo:_:fail")
    async def fail(self, ctx) -> None:
        await ctx.respond_error("boom", "deliberate failure")

    @slot("echo:_:chain")
    async def chain(self, ctx) -> None:
        await ctx.emit("echo:_:out", {"linked": True})


@pytest.fixture
async def echo(kernel: KernelServer):
    plugin = EchoPlugin()
    await plugin.start(url(kernel.port))
    try:
        yield plugin
    finally:
        await plugin.stop()


@pytest.mark.asyncio
async def test_hello_registration_and_error_autosubscribe(kernel: KernelServer) -> None:
    client = BusClient(url(kernel.port), SECOND_MANIFEST)
    errors: list[dict] = []
    seen = asyncio.Event()
    client.on_error(lambda value: (errors.append(value), seen.set()))
    await client.connect()
    await client.wait_registered()
    listing = await kernel.broker.get_retained("plugins:_:list")
    assert any(entry["conn"] == client.conn for entry in listing["value"])

    await client.publish("bad channel", 1)
    await asyncio.wait_for(seen.wait(), 2)
    assert errors[-1]["code"] == "invalid_channel"
    await client.close()


@pytest.mark.asyncio
async def test_subscribe_emit_set_roundtrip(kernel: KernelServer) -> None:
    producer = BusClient(url(kernel.port), ECHO_MANIFEST)
    consumer = BusClient(url(kernel.port), SECOND_MANIFEST)
    received: list[dict] = []
    got_live = asyncio.Event()

    async def handler(frame: dict) -> None:
        received.append(frame)
        got_live.set()

    await consumer.subscribe("demo:_:state", handler)
    await producer.connect()
    await consumer.connect()
    await producer.set("demo:_:state", {"v": 1})
    await asyncio.wait_for(got_live.wait(), 2)
    assert received[0]["value"] == {"v": 1}
    assert received[0]["origin"]["plugin"] == "echo"
    assert received[0]["type"] == "set"

    got_live.clear()
    await producer.publish("demo:_:state", {"v": 2})
    await asyncio.wait_for(got_live.wait(), 2)
    assert received[-1]["type"] == "publish"
    await producer.close()
    await consumer.close()


@pytest.mark.asyncio
async def test_rpc_success_and_error(kernel: KernelServer, echo: EchoPlugin) -> None:
    caller = BusClient(url(kernel.port), SECOND_MANIFEST)
    await caller.connect()
    result = await caller.request("echo:_:get", {"key": "theme"})
    assert result == {"echo": "theme"}
    with pytest.raises(RpcError) as raised:
        await caller.request("echo:_:fail", {"key": "x"})
    assert raised.value.code == "boom"
    await caller.close()


@pytest.mark.asyncio
async def test_rpc_timeout(kernel: KernelServer) -> None:
    caller = BusClient(url(kernel.port), SECOND_MANIFEST, request_timeout=0.2)
    await caller.connect()
    with pytest.raises(TimeoutError):
        await caller.request("nobody:_:get", {"key": "x"})
    await caller.close()


@pytest.mark.asyncio
async def test_causal_chain_trace_and_depth(kernel: KernelServer, echo: EchoPlugin) -> None:
    observer = BusClient(url(kernel.port), SECOND_MANIFEST)
    frames: list[dict] = []
    got = asyncio.Event()

    async def handler(frame: dict) -> None:
        frames.append(frame)
        got.set()

    await observer.subscribe("echo:_:out", handler)
    await observer.connect()
    caller = BusClient(url(kernel.port), {"id": "caller", "version": "0", "slots": {}, "emits": {}})
    await caller.connect()
    await caller.publish("echo:_:chain", {})
    await asyncio.wait_for(got.wait(), 2)
    out = frames[0]
    assert out["value"] == {"linked": True}
    assert out["depth"] == 1
    assert out["trace_id"]
    assert out["origin"]["plugin"] == "echo"
    await observer.close()
    await caller.close()


@pytest.mark.asyncio
async def test_reconnect_replays_subscriptions() -> None:
    # Pick a concrete port so the kernel can be restarted underneath the client.
    probe = socket.socket()
    probe.bind(("127.0.0.1", 0))
    port = probe.getsockname()[1]
    probe.close()

    server = KernelServer(ServerConfig(port=port))
    await server.start()
    client = BusClient(url(port), SECOND_MANIFEST, backoff_base=0.05, backoff_cap=0.2)
    received: list[dict] = []
    got = asyncio.Event()

    async def handler(frame: dict) -> None:
        received.append(frame)
        got.set()

    await client.subscribe("re:_:event", handler)
    await client.connect()
    await client.wait_registered()
    first_conn = client.conn

    # Kernel restart: client must survive, reconnect with a NEW conn, replay.
    await server.stop()
    server2 = KernelServer(ServerConfig(port=port))
    await server2.start()
    try:
        await asyncio.wait_for(client._connected.wait(), 5)
        assert client.conn != first_conn
        producer, _ = await open_client(server2, "producer")
        await producer.send(json.dumps({"type": "publish", "channel": "re:_:event", "value": 7}))
        await asyncio.wait_for(got.wait(), 2)
        assert received[-1]["value"] == 7
        await producer.close()
    finally:
        await client.close()
        await server2.stop()
