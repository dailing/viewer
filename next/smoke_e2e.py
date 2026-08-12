"""End-to-end smoke: kernel + supervisor + C1/C2/C3 + inspector as REAL processes.

Boot chain per framework section 9:
  kernel (only autostart: supervisor) -> supervisor reads registry ->
  spawns core plugin processes (backend/run) -> all hello ->
  SDK client drives RPC roundtrips -> kernel restart survival -> SIGINT.

Exits 0 with SMOKE-E2E-PASS on success, non-zero otherwise.
"""

from __future__ import annotations

import asyncio
import json
import os
import signal
import socket
import subprocess
import sys
import tempfile
from pathlib import Path

NEXT = Path(__file__).parent
PYTHON = NEXT / ".venv" / "bin" / "python"

sys.path.insert(0, str(NEXT))
from sdk import BusClient  # noqa: E402


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def make_plugin_package(root: Path, plugin_id: str, module: str) -> Path:
    package = root / plugin_id
    run = package / "backend"
    run.mkdir(parents=True)
    script = run / "run"
    script.write_text(
        f"#!/bin/sh\ncd {NEXT} && exec {PYTHON} -m {module} \"$@\"\n"
    )
    script.chmod(0o755)
    return package


async def main() -> None:
    port = free_port()
    kernel_ws = f"ws://127.0.0.1:{port}/ws"
    tmp = Path(tempfile.mkdtemp(prefix="viewer-e2e-"))

    registry = tmp / "registry.json"
    registry.write_text(
        json.dumps(
            {
                "plugins": [
                    {"id": "config-store", "path": str(make_plugin_package(tmp, "config-store", "plugins.configstore")), "enabled": True},
                    {"id": "instance-store", "path": str(make_plugin_package(tmp, "instance-store", "plugins.instancestore")), "enabled": True},
                    {"id": "file-service", "path": str(make_plugin_package(tmp, "file-service", "plugins.fileservice")), "enabled": True},
                    {"id": "bus-inspector", "path": str(make_plugin_package(tmp, "bus-inspector", "plugins.inspector")), "enabled": True},
                ]
            }
        )
    )

    env = {**os.environ, "VIEWER_MANAGED": "1"}
    kernel_proc = subprocess.Popen(
        [str(PYTHON), "-m", "kernel", "--host", "127.0.0.1", "--port", str(port)],
        cwd=NEXT,
    )
    supervisor_proc = None
    try:
        await asyncio.sleep(0.7)
        supervisor_proc = subprocess.Popen(
            [str(PYTHON), "-m", "plugins.supervisor", "--kernel-ws", kernel_ws, "--registry", str(registry)],
            cwd=NEXT,
            env=env,
        )

        client = BusClient(kernel_ws, {"id": "smoke", "version": "0", "slots": {}, "emits": {}}, backoff_base=0.05)
        await client.connect()

        # Wait until all four plugins are registered.
        async def registered() -> set[str]:
            seen: set[str] = set()
            done = asyncio.Event()

            async def watch(frame: dict) -> None:
                for entry in frame.get("value") or []:
                    seen.add(entry["manifest"]["id"])
                if {"config-store", "instance-store", "file-service", "bus-inspector"} <= seen:
                    done.set()

            await client.subscribe("plugins:_:list", watch)
            await asyncio.wait_for(done.wait(), 15)
            return seen

        seen = await registered()
        assert {"config-store", "instance-store", "file-service", "bus-inspector"} <= seen, seen
        print("boot chain: all core plugins registered ->", sorted(seen))

        # RPC roundtrips through the bus to real plugin processes.
        await client.request("config:_:set", {"plugin": "chat", "key": "roles", "value": ["x"]})
        assert await client.request("config:_:get", {"plugin": "chat", "key": "roles"}) == ["x"]
        await client.request(
            "instance:_:set",
            {"plugin": "chat", "instance": "1", "value": {"cwd": "/tmp"}},
        )
        state = await client.request("instance:_:get", {"plugin": "chat", "instance": "1"})
        assert state == {"cwd": "/tmp"}
        resolved = await client.request("file:_:resolve", {"path": str(registry)})
        assert resolved["size"] > 0
        snapshot = await client.request("bus-inspector:_:snapshot", {"limit": 5})
        assert snapshot["entries"], "inspector captured bus traffic"
        print("RPC roundtrips: config-store / instance-store / file-service / inspector OK")

        # Kernel restart survival (framework §9): plugins keep running and reconnect.
        kernel_proc.send_signal(signal.SIGINT)
        kernel_proc.wait(timeout=5)
        await asyncio.sleep(0.3)
        kernel_proc = subprocess.Popen(
            [str(PYTHON), "-m", "kernel", "--host", "127.0.0.1", "--port", str(port)],
            cwd=NEXT,
        )
        async with asyncio.timeout(15):
            while True:
                try:
                    probe = BusClient(kernel_ws, {"id": "probe", "version": "0", "slots": {}, "emits": {}}, reconnect=False)
                    await asyncio.wait_for(probe.connect(), 2)
                    value = await probe.request("config:_:get", {"plugin": "chat", "key": "roles"}, timeout=3)
                    assert value == ["x"], value  # config survived (plugin-side store)
                    await probe.close()
                    break
                except (OSError, ConnectionError, asyncio.TimeoutError):
                    await asyncio.sleep(0.2)
        print("kernel restart survival: plugins reconnected, state intact")

        # Graceful shutdown: SIGINT supervisor -> children terminated.
        supervisor_proc.send_signal(signal.SIGINT)
        supervisor_proc.wait(timeout=10)
        assert supervisor_proc.returncode == 0, supervisor_proc.returncode
        print("SIGINT: supervisor + children exited cleanly")
        print("SMOKE-E2E-PASS")
    finally:
        for proc in (supervisor_proc, kernel_proc):
            if proc is not None and proc.poll() is None:
                proc.send_signal(signal.SIGINT)
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()


if __name__ == "__main__":
    asyncio.run(main())
