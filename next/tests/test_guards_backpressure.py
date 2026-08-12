from __future__ import annotations

import asyncio
import json

import pytest

from conftest import open_client, receive_channel
from kernel.server import KernelServer, ServerConfig


@pytest.mark.asyncio
async def test_depth_and_oversize_are_nonfatal(kernel: KernelServer) -> None:
    client, greeting = await open_client(kernel, "guard")
    error_channel = f"_conn:{greeting['conn']}:error"
    await client.send(json.dumps({"type": "subscribe", "pattern": error_channel}))
    await client.send(json.dumps({
        "type": "publish", "channel": "guard:_:event", "value": 1,
        "trace_id": "trace", "depth": 8,
    }))
    assert (await receive_channel(client, error_channel))["value"]["code"] == "depth_exceeded"
    await client.send(json.dumps({
        "type": "publish", "channel": "guard:_:large", "value": "x" * (1024 * 1024)
    }))
    assert (await receive_channel(client, error_channel))["value"]["code"] == "frame_too_large"
    await client.send(json.dumps({"type": "publish", "channel": "guard:_:ok", "value": 1}))
    await client.send(json.dumps({"type": "subscribe", "pattern": "guard:_:ok"}))
    await client.close()


@pytest.mark.asyncio
async def test_slow_consumer_notice_over_priority_path() -> None:
    server = KernelServer(ServerConfig(port=0, outbound_queue_size=1))
    await server.start()
    try:
        slow, greeting = await open_client(server, "slow")
        producer, _ = await open_client(server, "producer")
        error_channel = f"_conn:{greeting['conn']}:error"
        await slow.send(json.dumps({"type": "subscribe", "pattern": ">"}))
        payload = "x" * 32768
        for index in range(200):
            await producer.send(json.dumps({
                "type": "publish", "channel": "flood:_:event", "value": [index, payload]
            }))
        notice = await receive_channel(slow, error_channel, timeout=5)
        assert notice["value"]["code"] == "slow_consumer"
        assert notice["value"]["detail"]["dropped"] >= 1
        await asyncio.gather(slow.close(), producer.close())
    finally:
        await server.stop()


@pytest.mark.asyncio
async def test_error_mailbox_is_reaped_on_disconnect(kernel: KernelServer) -> None:
    client, greeting = await open_client(kernel, "leaky")
    error_channel = f"_conn:{greeting['conn']}:error"
    await client.send(json.dumps({"type": "subscribe", "pattern": error_channel}))
    await client.send(json.dumps({"type": "publish", "channel": "bad channel", "value": 1}))
    notice = await receive_channel(client, error_channel)
    assert notice["value"]["code"] == "invalid_channel"
    assert (await kernel.broker.get_retained(error_channel))["value"]["code"] == "invalid_channel"
    await client.close()
    await asyncio.sleep(0.05)
    assert await kernel.broker.get_retained(error_channel) is None
