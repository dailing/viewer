#!/usr/bin/env python3
"""Black-box smoke test for the Go viewer.voice relay plugin."""

from __future__ import annotations

import asyncio
import base64
import importlib
import json
import os
import socket
import subprocess
import sys
import tempfile
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))

BusClient = importlib.import_module("sdk").BusClient


CALLER = {"id": "voice-smoke", "version": "0.1.0", "slots": {}, "emits": {}}


def free_port() -> int:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


async def wait_port(port: int, process: subprocess.Popen[Any]) -> None:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"process exited early with {process.returncode}")
        try:
            _, writer = await asyncio.open_connection("127.0.0.1", port)
            writer.close()
            await writer.wait_closed()
            return
        except OSError:
            await asyncio.sleep(0.05)
    raise TimeoutError(f"port {port} did not open")


async def wait_for(probe: Callable[[], Any], timeout: float = 10.0) -> Any:
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        if result := probe():
            return result
        await asyncio.sleep(0.03)
    raise TimeoutError("voice smoke condition timed out")


async def run() -> None:
    viewerd = Path(os.environ["VIEWERD_BIN"])
    mock_script = Path(__file__).with_name("mock_voice_service.py")
    kernel_port, service_port = free_port(), free_port()
    with tempfile.TemporaryDirectory(prefix="viewer-voice-smoke-") as raw_temp:
        temp = Path(raw_temp)
        start_file = temp / "start.json"
        viewerd_log = temp / "viewerd.log"
        mock_log = temp / "mock.log"
        with viewerd_log.open("wb") as viewer_output, mock_log.open("wb") as mock_output:
            mock = subprocess.Popen(  # noqa: ASYNC220
                [sys.executable, str(mock_script), "--port", str(service_port), "--start-file", str(start_file)],
                stdout=mock_output,
                stderr=subprocess.STDOUT,
            )
            await wait_port(service_port, mock)
            process = subprocess.Popen(  # noqa: ASYNC220
                [str(viewerd), "--plugins=config-store,voice", "--kernel-port", str(kernel_port), "--data-dir", str(temp / "data")],
                stdout=viewer_output,
                stderr=subprocess.STDOUT,
            )
        client = BusClient(f"ws://127.0.0.1:{kernel_port}/ws", CALLER, reconnect=False)
        events: list[dict[str, Any]] = []
        registry: list[dict[str, Any]] = []
        try:
            await wait_port(kernel_port, process)

            async def event_handler(frame: dict[str, Any]) -> None:
                events.append(frame)

            async def registry_handler(frame: dict[str, Any]) -> None:
                registry[:] = frame["value"]

            await client.subscribe("voice:*:event", event_handler)
            await client.subscribe("plugins:_:list", registry_handler)
            await client.connect()
            await wait_for(
                lambda: {entry["manifest"]["id"] for entry in registry}
                >= {"config-store", "voice"}
            )
            await client.request(
                "config:_:set",
                {
                    "plugin": "viewer-voice",
                    "key": "service_ws",
                    "value": f"ws://127.0.0.1:{service_port}/v1/voice/ws",
                },
            )
            started = await client.request(
                "voice:_:start",
                {"mime_type": "audio/webm;codecs=opus", "llm_refine": True},
            )
            rec_id = started["rec_id"]
            await client.publish(
                f"voice:{rec_id}:chunk",
                {"data": base64.b64encode(b"smoke-audio").decode("ascii")},
            )
            await client.publish(f"voice:{rec_id}:stop", {})
            final = await asyncio.wait_for(
                next_event(events, rec_id, "final"),
                timeout=15,
            )
            assert final["value"]["text"] == "smoke voice ok"
            start = json.loads(start_file.read_text(encoding="utf-8"))
            assert start["mimeType"] == "audio/webm;codecs=opus"
            assert start["llm_refine"] is True
            assert any(entry["manifest"]["id"] == "voice" for entry in registry)
            print("PASS voice start config and plugin registry")
            print("PASS base64 chunk relay and processing/final events")
            print("PASS voice smoke complete")
        except Exception:
            if viewerd_log.exists():
                print(viewerd_log.read_text(errors="replace")[-4000:], file=sys.stderr)
            if mock_log.exists():
                print(mock_log.read_text(errors="replace")[-4000:], file=sys.stderr)
            raise
        finally:
            await client.close()
            for child in (process, mock):
                child.terminate()
                try:
                    child.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait(timeout=5)


async def next_event(
    events: list[dict[str, Any]], rec_id: str, event_type: str
) -> dict[str, Any]:
    index = 0
    while True:
        while index < len(events):
            event = events[index]
            index += 1
            if (
                event["channel"] == f"voice:{rec_id}:event"
                and event["value"].get("type") == event_type
            ):
                return event
        await asyncio.sleep(0.02)


if __name__ == "__main__":
    asyncio.run(run())
