"""viewer.bus-inspector — debug plugin capturing all bus traffic (framework A.10).

Subscribes to `>` (fully open subscription, no privilege), filters its own
origin client-side, keeps a bounded ring buffer, streams matching frames on
`bus-inspector:_:matches`, and exposes pause/resume/clear/set-filter plus an
explicit paginated snapshot RPC (framework section 5.6 history rule). Emits a
`bus-inspector:_:stats` mailbox with rate/dropped counters; downsamples the
match stream when traffic exceeds the emit threshold.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import os
import signal
import time
from collections import deque
from typing import Any

from sdk import Ctx, Plugin, match, slot

logger = logging.getLogger(__name__)

MANIFEST: dict[str, Any] = {
    "id": "bus-inspector",
    "version": "0.1.0",
    "slots": {"set-filter": {}, "pause": {}, "resume": {}, "clear": {}, "snapshot": {}},
    "emits": {"matches": {}, "stats": {}},
}

MATCHES_CHANNEL = "bus-inspector:_:matches"
STATS_CHANNEL = "bus-inspector:_:stats"

# Snapshot replies are byte-budgeted (serialized JSON, newest-first) so they
# can never exceed the kernel's 1 MiB frame limit — an oversized reply is
# rejected by the kernel and the RPC caller hangs until timeout.
SNAPSHOT_BUDGET = 800_000


class BusInspectorPlugin(Plugin):
    manifest = MANIFEST

    def __init__(
        self,
        *,
        ring_size: int = 5000,
        emit_threshold: int = 200,
        stats_interval: float = 1.0,
        echo: bool = False,
        **client_kwargs: Any,
    ) -> None:
        super().__init__(**client_kwargs)
        self.ring: deque[dict[str, Any]] = deque(maxlen=ring_size)
        self.seq = 0
        self.filter: dict[str, Any] = {}
        self.paused = False
        self.echo = echo
        self.emit_threshold = emit_threshold
        self.stats_interval = stats_interval
        self.captured = 0
        self.emitted = 0
        self.dropped = 0
        self._window_start = time.monotonic()
        self._window_emitted = 0
        self._rate = 0.0
        self._window_captured = 0
        self._stats_task: asyncio.Task[None] | None = None

    # -------------------------------------------------------------- lifecycle

    async def on_start(self) -> None:
        self._stats_task = asyncio.create_task(self._stats_loop())

    async def on_stop(self) -> None:
        if self._stats_task is not None:
            self._stats_task.cancel()

    # ---------------------------------------------------------------- capture

    @slot(">")
    async def capture(self, ctx: Ctx) -> None:
        origin = ctx.origin or {}
        # Echo prevention is the consumer's job (framework section 5.3).
        if origin.get("plugin") == self.manifest["id"]:
            return
        self.seq += 1
        entry = {
            "seq": self.seq,
            "ts": ctx.ts,
            "type": ctx.frame.get("type"),
            "channel": ctx.channel,
            "origin": origin,
            "trace_id": ctx.trace_id,
            "depth": ctx.depth,
            "value": ctx.value,
        }
        self.ring.append(entry)
        self.captured += 1
        self._window_captured += 1
        if self.echo:
            rendered = json.dumps(entry["value"], separators=(",", ":"), ensure_ascii=False)
            print(f"{entry['ts']} {entry['type']:>8} {entry['channel']} "
                  f"{origin.get('plugin', '?')} {rendered[:200]}", flush=True)
        if self.paused or not self._matches(entry):
            return
        now = time.monotonic()
        if now - self._window_start >= 1.0:
            self._window_start = now
            self._window_emitted = 0
        if self._window_emitted >= self.emit_threshold:
            self.dropped += 1
            return
        self._window_emitted += 1
        self.emitted += 1
        # The match stream is a fresh observation channel, NOT a causal
        # continuation of the captured frame — otherwise capturing a depth-7
        # frame would emit at depth 8 and hit the kernel's loop guard.
        assert self.client is not None
        await self.client.publish(MATCHES_CHANNEL, entry)

    def _matches(self, entry: dict[str, Any]) -> bool:
        f = self.filter
        if not f:
            return True
        if f.get("channel") and not match(f["channel"], entry["channel"]):
            return False
        if f.get("type") and entry["type"] != f["type"]:
            return False
        if f.get("origin") and entry["origin"].get("plugin") != f["origin"]:
            return False
        if f.get("trace_id") and entry.get("trace_id") != f["trace_id"]:
            return False
        if f.get("text"):
            haystack = json.dumps(entry["value"], ensure_ascii=False)
            if f["text"] not in haystack:
                return False
        return True

    # ------------------------------------------------------------------ slots

    @slot("bus-inspector:_:set-filter")
    async def set_filter(self, ctx: Ctx) -> None:
        value = ctx.value if isinstance(ctx.value, dict) else {}
        allowed = {"channel", "type", "origin", "trace_id", "text"}
        self.filter = {key: value[key] for key in allowed if value.get(key)}
        await self._publish_stats()
        await ctx.respond({"filter": self.filter})

    @slot("bus-inspector:_:pause")
    async def pause(self, ctx: Ctx) -> None:
        self.paused = True
        await self._publish_stats()
        await ctx.respond({"paused": True})

    @slot("bus-inspector:_:resume")
    async def resume(self, ctx: Ctx) -> None:
        self.paused = False
        await self._publish_stats()
        await ctx.respond({"paused": False})

    @slot("bus-inspector:_:clear")
    async def clear(self, ctx: Ctx) -> None:
        self.ring.clear()
        await ctx.respond({"cleared": True})

    @slot("bus-inspector:_:snapshot")
    async def snapshot(self, ctx: Ctx) -> None:
        """Explicit paginated history (framework section 5.6): ``limit`` +
        ``before_seq`` cursor; never an implicit read of the live stream."""

        value = ctx.value if isinstance(ctx.value, dict) else {}
        limit = int(value.get("limit", 100))
        before_seq = value.get("before_seq")
        # Newest-first under a serialized-size budget; keep at least the
        # newest matching entry regardless of size.
        entries: list[dict[str, Any]] = []
        budget = SNAPSHOT_BUDGET
        for entry in reversed(self.ring):
            if len(entries) >= limit:
                break
            if before_seq is not None and entry["seq"] >= before_seq:
                continue
            size = len(json.dumps(entry))
            if entries and size > budget:
                break
            entries.append(entry)
            budget -= size
        entries.reverse()
        await ctx.respond({"entries": entries})

    # ------------------------------------------------------------------ stats

    async def _stats_loop(self) -> None:
        while True:
            await asyncio.sleep(self.stats_interval)
            self._rate = self._window_captured / self.stats_interval
            self._window_captured = 0
            await self._publish_stats()

    async def _publish_stats(self) -> None:
        if self.client is None or not self.client.connected:
            return
        await self.client.set(
            STATS_CHANNEL,
            {
                "captured": self.captured,
                "emitted": self.emitted,
                "dropped": self.dropped,
                "rate_per_sec": self._rate,
                "paused": self.paused,
                "filter": self.filter,
                "ring_size": self.ring.maxlen,
                "ring_used": len(self.ring),
            },
        )

    # ------------------------------------------------------------- entrypoint

    def run(self) -> None:
        parser = argparse.ArgumentParser(description="Viewer bus-inspector debug plugin")
        parser.add_argument("--kernel-ws", required=True)
        parser.add_argument("--ring-size", type=int, default=5000)
        parser.add_argument("--echo", action="store_true", help="print captured frames to stdout")
        args = parser.parse_args()
        logging.basicConfig(
            level=logging.INFO,
            format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        )
        self.ring = deque(maxlen=args.ring_size)
        self.echo = args.echo
        managed = os.environ.get("VIEWER_MANAGED") == "1"

        async def main() -> None:
            await self.start(args.kernel_ws, managed=managed)
            stopped = asyncio.Event()
            loop = asyncio.get_running_loop()
            for signum in (signal.SIGINT, signal.SIGTERM):
                loop.add_signal_handler(signum, stopped.set)
            await stopped.wait()
            await self.stop()

        asyncio.run(main())
