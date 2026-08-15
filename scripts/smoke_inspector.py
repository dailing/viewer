#!/usr/bin/env python3
"""Black-box smoke test for the Go bus-inspector plugin."""

from __future__ import annotations

import asyncio
import os
import signal
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from sdk import BusClient  # noqa: E402

PORT = int(os.environ.get("VIEWER_INSPECTOR_SMOKE_PORT", "29372"))
URL = f"ws://127.0.0.1:{PORT}/ws"
VIEWERD = Path(os.environ.get("VIEWERD_BIN", "/tmp/viewerd"))
INSPECTOR = Path(os.environ.get("VIEWER_INSPECTOR_BIN", "/tmp/viewer-inspector"))


def manifest(plugin_id: str) -> dict[str, Any]:
    return {"id": plugin_id, "version": "1.0.0", "slots": {}, "emits": {}}


async def wait_port(process: subprocess.Popen[Any]) -> None:
    for _ in range(100):
        if process.poll() is not None:
            raise AssertionError(f"kernel exited early: {process.returncode}")
        try:
            reader, writer = await asyncio.open_connection("127.0.0.1", PORT)
            del reader
            writer.close(); await writer.wait_closed(); return
        except OSError:
            await asyncio.sleep(0.05)
    raise AssertionError("kernel did not start")


def stop_process(process: subprocess.Popen[Any] | None) -> None:
    if process is None or process.poll() is not None:
        return
    try:
        os.killpg(process.pid, signal.SIGTERM); process.wait(timeout=4)
    except (ProcessLookupError, subprocess.TimeoutExpired):
        try: os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError: pass
        process.wait(timeout=4)


async def wait_plugin(client: BusClient, plugin_id: str) -> None:
    seen = asyncio.Event()
    async def handler(frame: dict[str, Any]) -> None:
        if any(entry.get("id") == plugin_id for entry in frame.get("value", [])):
            seen.set()
    await client.subscribe("plugins:_:list", handler)
    await asyncio.wait_for(seen.wait(), 5)
    await client.unsubscribe("plugins:_:list", handler)


async def main() -> int:
    if not VIEWERD.is_file() or not INSPECTOR.is_file():
        raise AssertionError("build /tmp/viewerd and /tmp/viewer-inspector first")
    kernel = inspector = None
    clients: list[BusClient] = []
    with tempfile.TemporaryDirectory(prefix="viewer-inspector-smoke-") as raw:
        temp = Path(raw)
        kernel_log = (temp / "kernel.log").open("w+")
        inspector_log = (temp / "inspector.log").open("w+")
        try:
            kernel = subprocess.Popen([str(VIEWERD), "--plugins=none", "--kernel-port", str(PORT), "--data-dir", str(temp / "data")], stdout=kernel_log, stderr=subprocess.STDOUT, start_new_session=True)
            await wait_port(kernel)
            inspector = subprocess.Popen([str(INSPECTOR), "--kernel-ws", URL, "--ring-size", "5000"], stdout=inspector_log, stderr=subprocess.STDOUT, start_new_session=True)

            observer = BusClient(URL, manifest("inspector-observer"), reconnect=False)
            producer = BusClient(URL, manifest("inspector-producer"), reconnect=False)
            matches: asyncio.Queue[dict[str, Any]] = asyncio.Queue()
            stats: asyncio.Queue[dict[str, Any]] = asyncio.Queue()
            async def match_handler(frame: dict[str, Any]) -> None: await matches.put(frame)
            async def stats_handler(frame: dict[str, Any]) -> None: await stats.put(frame["value"])
            await observer.subscribe("bus-inspector:_:matches", match_handler)
            await observer.subscribe("bus-inspector:_:stats", stats_handler)
            await asyncio.gather(observer.connect(), producer.connect())
            clients.extend((observer, producer))
            await wait_plugin(observer, "bus-inspector")

            await observer.request("bus-inspector:_:clear", timeout=3)
            await observer.request("bus-inspector:_:set-filter", {"channel": "traffic:*:event", "type": "publish", "origin": "inspector-producer", "text": "needle"}, timeout=3)
            while not matches.empty():
                matches.get_nowait()
            await producer.publish("traffic:one:event", {"text": "miss"})
            await producer.publish("other:one:event", {"text": "needle"})
            await producer.publish("traffic:one:event", {"text": "needle", "n": 1}, trace_id="trace-smoke", depth=7)
            matched_frame = await asyncio.wait_for(matches.get(), 3)
            entry = matched_frame["value"]
            assert entry["channel"] == "traffic:one:event" and entry["trace_id"] == "trace-smoke" and entry["depth"] == 7
            assert matched_frame["depth"] == 0
            print("PASS capture, compound filter, and fresh match depth")

            await observer.request("bus-inspector:_:pause", timeout=3)
            while not matches.empty(): matches.get_nowait()
            await producer.publish("traffic:two:event", {"text": "needle", "phase": "paused"})
            try:
                await asyncio.wait_for(matches.get(), 0.35)
            except TimeoutError:
                pass
            else:
                raise AssertionError("paused inspector emitted a match")
            await observer.request("bus-inspector:_:resume", timeout=3)
            await producer.publish("traffic:two:event", {"text": "needle", "phase": "resumed"})
            resumed = await asyncio.wait_for(matches.get(), 3)
            assert resumed["value"]["value"]["phase"] == "resumed"
            print("PASS pause stops stream and resume restores it")

            await observer.request("bus-inspector:_:set-filter", {}, timeout=3)
            for number in range(6):
                await producer.publish("paging:one:event", {"page": number})
            await asyncio.sleep(0.15)
            first = await observer.request("bus-inspector:_:snapshot", {"limit": 3}, timeout=3)
            first_entries = first["entries"]
            assert len(first_entries) == 3
            assert [item["seq"] for item in first_entries] == sorted((item["seq"] for item in first_entries), reverse=True)
            cursor = first_entries[-1]["seq"]
            second = await observer.request("bus-inspector:_:snapshot", {"limit": 3, "before_seq": cursor}, timeout=3)
            assert second["entries"] and all(item["seq"] < cursor for item in second["entries"])
            print("PASS newest-first snapshot cursor pagination")

            snapshot = await observer.request("bus-inspector:_:snapshot", {"limit": 5000}, timeout=5)
            assert snapshot["entries"] and all(item["origin"].get("plugin") != "bus-inspector" for item in snapshot["entries"])
            print("PASS client-side self-echo prevention")

            retained = BusClient(URL, manifest("inspector-stats-reader"), reconnect=False)
            retained_stats: asyncio.Queue[dict[str, Any]] = asyncio.Queue()
            async def retained_handler(frame: dict[str, Any]) -> None: await retained_stats.put(frame["value"])
            await retained.subscribe("bus-inspector:_:stats", retained_handler)
            await retained.connect(); clients.append(retained)
            current = await asyncio.wait_for(retained_stats.get(), 3)
            assert current["captured"] > 0 and current["ring_used"] > 0 and current["ring_size"] == 5000
            assert current["paused"] is False and current["filter"] == {}
            print("PASS retained stats mailbox")

            await observer.request("bus-inspector:_:set-filter", {"channel": "burst:_:event"}, timeout=3)
            await asyncio.sleep(1.05)
            for number in range(230): await producer.publish("burst:_:event", number)
            async with asyncio.timeout(3):
                while True:
                    current = await stats.get()
                    if current["dropped"] > 0: break
            print("PASS 200/sec match downsampling")
            print("RESULT 6/6 passed")
            return 0
        except Exception:
            for name, handle in (("kernel", kernel_log), ("inspector", inspector_log)):
                handle.flush(); handle.seek(0)
                print(f"--- {name} log tail ---", file=sys.stderr); print(handle.read()[-6000:], file=sys.stderr)
            raise
        finally:
            await asyncio.gather(*(client.close() for client in clients), return_exceptions=True)
            stop_process(inspector); stop_process(kernel)
            kernel_log.close(); inspector_log.close()


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
