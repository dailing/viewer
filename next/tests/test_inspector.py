"""bus-inspector tests against a real kernel."""

from __future__ import annotations

import asyncio
from typing import Any

import pytest

from kernel.server import KernelServer
from plugins.inspector import BusInspectorPlugin
from sdk import BusClient

PRODUCER_MANIFEST = {"id": "producer", "version": "0", "slots": {}, "emits": {}}
CALLER_MANIFEST = {"id": "caller", "version": "0", "slots": {}, "emits": {}}


def url(port: int) -> str:
    return f"ws://127.0.0.1:{port}/ws"


async def wait_for(predicate, timeout: float = 5.0) -> None:
    async with asyncio.timeout(timeout):
        while not predicate():
            await asyncio.sleep(0.03)


@pytest.fixture
async def inspector(kernel: KernelServer):
    plugin = BusInspectorPlugin(stats_interval=0.1)
    await plugin.start(url(kernel.port))
    try:
        yield plugin
    finally:
        await plugin.stop()


@pytest.fixture
async def producer(kernel: KernelServer):
    client = BusClient(url(kernel.port), PRODUCER_MANIFEST)
    await client.connect()
    try:
        yield client
    finally:
        await client.close()


@pytest.mark.asyncio
async def test_captures_all_and_filters_own_origin(
    inspector: BusInspectorPlugin, producer: BusClient
) -> None:
    await producer.publish("foo:_:event", {"v": 1})
    await wait_for(lambda: any(e["channel"] == "foo:_:event" for e in inspector.ring))
    # The inspector's own stats/matches publishes must never enter the ring.
    await asyncio.sleep(0.3)
    assert all(e["origin"].get("plugin") != "bus-inspector" for e in inspector.ring)


@pytest.mark.asyncio
async def test_matches_stream_and_channel_text_filter(
    kernel: KernelServer, inspector: BusInspectorPlugin, producer: BusClient
) -> None:
    caller = BusClient(url(kernel.port), CALLER_MANIFEST)
    hits: list[dict[str, Any]] = []
    got_hit = asyncio.Event()

    async def on_match(frame: dict) -> None:
        hits.append(frame["value"])
        got_hit.set()

    await caller.subscribe("bus-inspector:_:matches", on_match)
    await caller.connect()

    result = await caller.request("bus-inspector:_:set-filter", {"channel": "foo:*"})
    assert result["filter"] == {"channel": "foo:*"}
    # The set-filter request frame itself was captured under the previous
    # (empty) filter and legitimately streamed as a match — reset bookkeeping.
    hits.clear()
    got_hit.clear()

    await producer.publish("bar:_:event", {"v": "miss"})
    await producer.publish("foo:1:event", {"v": "hit"})
    await asyncio.wait_for(got_hit.wait(), 2)
    assert [h["channel"] for h in hits] == ["foo:1:event"]

    # Text filter narrows further.
    got_hit.clear()
    hits.clear()
    await caller.request("bus-inspector:_:set-filter", {"channel": "foo:*", "text": "needle"})
    await producer.publish("foo:1:event", {"v": "haystack"})
    await producer.publish("foo:2:event", {"v": "find the needle"})
    await asyncio.wait_for(got_hit.wait(), 2)
    assert [h["channel"] for h in hits] == ["foo:2:event"]
    await caller.close()


@pytest.mark.asyncio
async def test_pause_keeps_capturing_clear_empties(
    kernel: KernelServer, inspector: BusInspectorPlugin, producer: BusClient
) -> None:
    caller = BusClient(url(kernel.port), CALLER_MANIFEST)
    hits: list[dict[str, Any]] = []

    async def on_match(frame: dict) -> None:
        hits.append(frame["value"])

    await caller.subscribe("bus-inspector:_:matches", on_match)
    await caller.connect()

    await caller.request("bus-inspector:_:pause", {})
    await producer.publish("paused:_:event", {"v": 1})
    await wait_for(lambda: any(e["channel"] == "paused:_:event" for e in inspector.ring))
    await asyncio.sleep(0.2)
    assert not any(h["channel"] == "paused:_:event" for h in hits)

    await caller.request("bus-inspector:_:resume", {})
    resumed = asyncio.Event()

    async def on_match2(frame: dict) -> None:
        if frame["value"]["channel"] == "resumed:_:event":
            resumed.set()

    await caller.subscribe("bus-inspector:_:matches", on_match2)
    await producer.publish("resumed:_:event", {"v": 2})
    await asyncio.wait_for(resumed.wait(), 2)

    await caller.request("bus-inspector:_:clear", {})
    assert len(inspector.ring) == 0
    await caller.close()


@pytest.mark.asyncio
async def test_snapshot_explicit_paging(
    inspector: BusInspectorPlugin, producer: BusClient, kernel: KernelServer
) -> None:
    for index in range(5):
        await producer.publish("page:_:event", {"i": index})
    await wait_for(lambda: sum(1 for e in inspector.ring if e["channel"] == "page:_:event") == 5)

    caller = BusClient(url(kernel.port), CALLER_MANIFEST)
    await caller.connect()
    # Snapshot pages the raw ring tail (unfiltered): the ring also holds the
    # caller's own lifecycle/registry frames, so filter client-side.
    page = await caller.request("bus-inspector:_:snapshot", {"limit": 50})
    pages = [e for e in page["entries"] if e["channel"] == "page:_:event"]
    assert [e["value"]["i"] for e in pages] == [0, 1, 2, 3, 4]
    cursor = pages[2]["seq"]
    older = await caller.request(
        "bus-inspector:_:snapshot", {"limit": 50, "before_seq": cursor}
    )
    older_values = [e["value"]["i"] for e in older["entries"] if e["channel"] == "page:_:event"]
    assert older_values == [0, 1]
    await caller.close()


@pytest.mark.asyncio
async def test_stats_mailbox_and_downsample(kernel: KernelServer, producer: BusClient) -> None:
    inspector = BusInspectorPlugin(stats_interval=0.05, emit_threshold=3)
    await inspector.start(url(kernel.port))
    try:
        await wait_for(lambda: inspector.captured >= 1)  # plugins:_:list etc.
        for index in range(10):
            await producer.publish("flood:_:event", {"i": index})
        await wait_for(lambda: inspector.dropped >= 1)
        stats = None
        async with asyncio.timeout(5):
            while stats is None:
                stats = await kernel.broker.get_retained("bus-inspector:_:stats")
                await asyncio.sleep(0.03)
        value = stats["value"]
        assert value["dropped"] >= 1
        assert value["emitted"] < value["captured"]
        assert value["paused"] is False
        assert value["ring_size"] == 5000
    finally:
        await inspector.stop()
