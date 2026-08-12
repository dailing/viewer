from __future__ import annotations

import asyncio
import json

import pytest

from conftest import open_client, receive_channel
from kernel.server import KernelServer


@pytest.mark.asyncio
async def test_set_retained_replace_and_event_not_retained(kernel: KernelServer) -> None:
    producer, _ = await open_client(kernel, "producer")
    await producer.send(json.dumps({"type": "set", "channel": "demo:_:state", "value": {"a": 1}}))
    await producer.send(json.dumps({"type": "set", "channel": "demo:_:state", "value": {"b": 2}}))
    await producer.send(json.dumps({"type": "publish", "channel": "demo:_:event", "value": "past"}))
    await asyncio.sleep(0)

    subscriber, _ = await open_client(kernel, "subscriber")
    await subscriber.send(json.dumps({"type": "subscribe", "pattern": "demo:_"}))
    retained = await receive_channel(subscriber, "demo:_:state")
    assert retained["value"] == {"b": 2}
    with pytest.raises(TimeoutError):
        await receive_channel(subscriber, "demo:_:event", timeout=0.1)
    await producer.close()
    await subscriber.close()


@pytest.mark.asyncio
async def test_atomic_handoff_precedes_live_without_loss_or_duplicates(kernel: KernelServer) -> None:
    producer, _ = await open_client(kernel, "producer")
    subscriber, _ = await open_client(kernel, "subscriber")
    await producer.send(json.dumps({"type": "set", "channel": "race:_:state", "value": 0}))
    await asyncio.sleep(0)
    await asyncio.gather(
        subscriber.send(json.dumps({"type": "subscribe", "pattern": "race:_:state"})),
        producer.send(json.dumps({"type": "set", "channel": "race:_:state", "value": 1})),
    )
    first = await receive_channel(subscriber, "race:_:state")
    if first["value"] == 0:
        second = await receive_channel(subscriber, "race:_:state")
        assert second["value"] == 1
    else:
        assert first["value"] == 1
    with pytest.raises(TimeoutError):
        await receive_channel(subscriber, "race:_:state", timeout=0.1)
    await producer.close()
    await subscriber.close()


@pytest.mark.asyncio
async def test_unsubscribe_and_independent_subscribers(kernel: KernelServer) -> None:
    producer, _ = await open_client(kernel, "producer")
    first, _ = await open_client(kernel, "first")
    second, _ = await open_client(kernel, "second")
    for websocket in (first, second):
        await websocket.send(json.dumps({"type": "subscribe", "pattern": "fan:*:event"}))
    await producer.send(json.dumps({"type": "publish", "channel": "fan:1:event", "value": 1}))
    assert (await receive_channel(first, "fan:1:event"))["value"] == 1
    assert (await receive_channel(second, "fan:1:event"))["value"] == 1
    await first.send(json.dumps({"type": "unsubscribe", "pattern": "fan:*:event"}))
    await producer.send(json.dumps({"type": "publish", "channel": "fan:1:event", "value": 2}))
    assert (await receive_channel(second, "fan:1:event"))["value"] == 2
    with pytest.raises(TimeoutError):
        await receive_channel(first, "fan:1:event", timeout=0.1)
    await asyncio.gather(producer.close(), first.close(), second.close())
