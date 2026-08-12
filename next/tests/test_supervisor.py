"""C0 supervisor tests: real subprocesses, real kernel, no mocks."""

from __future__ import annotations

import asyncio
import json
import sys
import textwrap
from pathlib import Path

import pytest

from kernel.server import KernelServer
from plugins.supervisor import SupervisorPlugin
from sdk import RpcError

FAKE_PLUGIN = textwrap.dedent(
    """
    import argparse, asyncio, json, uuid
    import websockets

    async def main():
        parser = argparse.ArgumentParser()
        parser.add_argument("--kernel-ws", required=True)
        args = parser.parse_args()
        hello = {
            "type": "hello", "protocol_version": 1, "conn": str(uuid.uuid4()),
            "manifest": {"id": "fake", "version": "0", "slots": {}, "emits": {}},
            "managed": True,
        }
        async with websockets.connect(args.kernel_ws) as ws:
            await ws.send(json.dumps(hello))
            await asyncio.Event().wait()

    asyncio.run(main())
    """
)


def make_plugin(tmp_path: Path, plugin_id: str, body: str) -> Path:
    package = tmp_path / plugin_id
    backend = package / "backend"
    backend.mkdir(parents=True)
    script = backend / "plugin_main.py"
    script.write_text(body)
    run = backend / "run"
    run.write_text(f"#!/bin/sh\nexec {sys.executable} {script} \"$@\"\n")
    run.chmod(0o755)
    return package


def make_registry(tmp_path: Path, entries: list[dict]) -> Path:
    registry = tmp_path / "registry.json"
    registry.write_text(json.dumps({"plugins": entries}))
    return registry


async def wait_states(kernel: KernelServer, predicate, timeout: float = 5.0) -> dict:
    async with asyncio.timeout(timeout):
        while True:
            retained = await kernel.broker.get_retained("supervisor:_:states")
            if retained is not None and predicate(retained["value"]):
                return retained["value"]
            await asyncio.sleep(0.05)


@pytest.fixture
async def supervisor(kernel: KernelServer, tmp_path: Path):
    package = make_plugin(tmp_path, "fake", FAKE_PLUGIN)
    registry = make_registry(tmp_path, [{"id": "fake", "path": str(package)}])
    plugin = SupervisorPlugin(
        registry_path=registry,
        log_dir=tmp_path / "logs",
        backoff_base=0.05,
        backoff_cap=0.2,
        breaker_max_crashes=3,
        breaker_window=5.0,
    )
    await plugin.start(f"ws://127.0.0.1:{kernel.port}/ws")
    try:
        yield plugin
    finally:
        await plugin.stop()


@pytest.mark.asyncio
async def test_spawn_reaches_running_and_lists_managed(
    kernel: KernelServer, supervisor: SupervisorPlugin
) -> None:
    states = await wait_states(kernel, lambda v: v.get("fake", {}).get("state") == "running")
    pid = states["fake"]["pid"]
    assert isinstance(pid, int)
    listing = await kernel.broker.get_retained("plugins:_:list")
    fake = next(e for e in listing["value"] if e["manifest"]["id"] == "fake")
    assert fake["managed"] is True


@pytest.mark.asyncio
async def test_crash_publishes_lifecycle_and_restarts(
    kernel: KernelServer, supervisor: SupervisorPlugin
) -> None:
    states = await wait_states(kernel, lambda v: v.get("fake", {}).get("state") == "running")
    old_pid = states["fake"]["pid"]

    lifecycle: list[dict] = []
    got_restart = asyncio.Event()

    async def watcher(frame: dict) -> None:
        lifecycle.append(frame["value"])
        if frame["value"].get("state") == "restarted":
            got_restart.set()

    assert supervisor.client is not None
    await supervisor.client.subscribe("plugins:fake:lifecycle", watcher)

    managed = supervisor._managed["fake"]
    assert managed.process is not None
    managed.process.kill()

    await asyncio.wait_for(got_restart.wait(), 5)
    sequence = [item["state"] for item in lifecycle]
    # Kernel owns activated/deactivated; supervisor owns crashed/restarted.
    assert sequence[0] == "deactivated"
    assert sequence.index("crashed") < sequence.index("restarted")
    states = await wait_states(
        kernel, lambda v: v.get("fake", {}).get("state") == "running"
    )
    assert states["fake"]["pid"] != old_pid


@pytest.mark.asyncio
async def test_crash_loop_opens_circuit_breaker(
    kernel: KernelServer, tmp_path: Path
) -> None:
    package = make_plugin(tmp_path, "crashy", "import sys; sys.exit(1)\n")
    registry = make_registry(tmp_path, [{"id": "crashy", "path": str(package)}])
    plugin = SupervisorPlugin(
        registry_path=registry,
        log_dir=tmp_path / "logs",
        backoff_base=0.02,
        backoff_cap=0.05,
        breaker_max_crashes=3,
        breaker_window=5.0,
    )
    await plugin.start(f"ws://127.0.0.1:{kernel.port}/ws")
    try:
        states = await wait_states(
            kernel, lambda v: v.get("crashy", {}).get("state") == "broken"
        )
        assert states["crashy"]["crashes"] == 3
        crashes = states["crashy"]["crashes"]
        # Breaker open: no further restart attempts.
        await asyncio.sleep(0.3)
        retained = await kernel.broker.get_retained("supervisor:_:states")
        assert retained["value"]["crashy"]["crashes"] == crashes
    finally:
        await plugin.stop()


@pytest.mark.asyncio
async def test_restart_rpc_replaces_process(
    kernel: KernelServer, supervisor: SupervisorPlugin
) -> None:
    from sdk import BusClient

    states = await wait_states(kernel, lambda v: v.get("fake", {}).get("state") == "running")
    old_pid = states["fake"]["pid"]

    caller = BusClient(
        f"ws://127.0.0.1:{kernel.port}/ws",
        {"id": "caller", "version": "0", "slots": {}, "emits": {}},
    )
    await caller.connect()
    result = await caller.request("supervisor:_:restart", {"id": "fake"}, timeout=5)
    assert result["id"] == "fake"
    assert result["pid"] != old_pid
    with pytest.raises(RpcError) as raised:
        await caller.request("supervisor:_:restart", {"id": "ghost"}, timeout=5)
    assert raised.value.code == "not_found"
    await caller.close()
