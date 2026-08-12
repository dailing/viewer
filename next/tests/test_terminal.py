"""viewer.terminal tests against a real kernel + real PTYs."""

from __future__ import annotations

import asyncio

import pytest

from kernel.server import KernelServer
from plugins.terminal import TerminalPlugin
from sdk import BusClient, RpcError


def url(port: int) -> str:
    return f"ws://127.0.0.1:{port}/ws"


CALLER = {"id": "caller", "version": "0", "slots": {}, "emits": {}}


@pytest.fixture
async def caller(kernel: KernelServer):
    client = BusClient(url(kernel.port), CALLER)
    await client.connect()
    try:
        yield client
    finally:
        await client.close()


@pytest.fixture
async def terminal(kernel: KernelServer):
    plugin = TerminalPlugin()
    await plugin.start(url(kernel.port))
    try:
        yield plugin
    finally:
        await plugin.stop()


async def wait_for(predicate, timeout: float = 5.0):
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        value = predicate()
        if value:
            return value
        await asyncio.sleep(0.05)
    raise TimeoutError("condition not met within timeout")


async def wait_for_status(
    kernel: KernelServer, term_id: str, state: str, timeout: float = 5.0
) -> dict:
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        retained = await kernel.broker.get_retained(f"terminal:{term_id}:status")
        if retained and retained["value"].get("state") == state:
            return retained["value"]
        await asyncio.sleep(0.05)
    raise TimeoutError(f"terminal {term_id} did not reach state {state!r}")


@pytest.mark.asyncio
async def test_create_spawns_pty_and_publishes_status(
    kernel: KernelServer, caller: BusClient, terminal: TerminalPlugin
) -> None:
    result = await caller.request("terminal:_:create", {"cwd": "/tmp", "shell": "/bin/bash"})
    term_id = result["id"]

    status = await kernel.broker.get_retained(f"terminal:{term_id}:status")
    assert status["value"]["state"] == "running"
    assert status["value"]["cwd"] == "/tmp"
    assert status["value"]["pid"] > 0

    listed = await caller.request("terminal:_:list")
    assert [entry["id"] for entry in listed] == [term_id]


@pytest.mark.asyncio
async def test_create_rejects_bad_cwd(caller: BusClient, terminal: TerminalPlugin) -> None:
    with pytest.raises(RpcError, match="bad_cwd"):
        await caller.request("terminal:_:create", {"cwd": "/no/such/dir/xyz"})


@pytest.mark.asyncio
async def test_input_produces_output_events_and_ring(
    kernel: KernelServer, caller: BusClient, terminal: TerminalPlugin
) -> None:
    term_id = (await caller.request("terminal:_:create", {"cwd": "/tmp", "shell": "/bin/bash"}))["id"]

    received: list[dict] = []
    event = asyncio.Event()

    async def on_output(frame: dict) -> None:
        received.append(frame["value"])
        if "hello-marker" in "".join(entry["data"] for entry in received):
            event.set()

    await caller.subscribe(f"terminal:{term_id}:output", on_output)
    await caller.request(f"terminal:{term_id}:input", {"data": "echo hello-marker\n"})

    await asyncio.wait_for(event.wait(), timeout=5.0)
    joined = "".join(entry["data"] for entry in received)
    assert "hello-marker" in joined
    assert all(entry["ts"] > 0 for entry in received)
    assert [entry["seq"] for entry in received] == sorted(entry["seq"] for entry in received)

    # Ring is the source of truth for history (framework 5.6): explicit RPC.
    snapshot = await caller.request(f"terminal:{term_id}:snapshot", {"limit": 100})
    assert "hello-marker" in "".join(entry["data"] for entry in snapshot["entries"])


@pytest.mark.asyncio
async def test_resize_takes_effect_in_pty(
    caller: BusClient, terminal: TerminalPlugin
) -> None:
    term_id = (await caller.request("terminal:_:create", {"cwd": "/tmp", "shell": "/bin/bash"}))["id"]

    received: list[str] = []
    event = asyncio.Event()

    async def on_output(frame: dict) -> None:
        received.append(frame["value"]["data"])
        if "43 132" in "".join(received):  # stty size prints "rows cols"
            event.set()

    await caller.subscribe(f"terminal:{term_id}:output", on_output)
    await caller.publish(f"terminal:{term_id}:resize", {"cols": 132, "rows": 43})
    await caller.request(f"terminal:{term_id}:input", {"data": "stty size\n"})

    await asyncio.wait_for(event.wait(), timeout=5.0)
    status = await caller.request("terminal:_:list")
    assert status[0]["cols"] == 132
    assert status[0]["rows"] == 43


@pytest.mark.asyncio
async def test_kill_terminates_pty_and_updates_status(
    kernel: KernelServer, caller: BusClient, terminal: TerminalPlugin
) -> None:
    term_id = (await caller.request("terminal:_:create", {"cwd": "/tmp", "shell": "/bin/bash"}))["id"]
    pid = (await caller.request("terminal:_:list"))[0]["pid"]

    await caller.request(f"terminal:{term_id}:kill")
    status = await wait_for_status(kernel, term_id, "killed")
    assert status["exit_code"] is not None
    # The child process is gone.
    import os

    with pytest.raises(ProcessLookupError):
        os.kill(pid, 0)
    # Slots on a killed/removed terminal report cleanly.
    with pytest.raises(RpcError, match="no_such_terminal"):
        await caller.request(f"terminal:{term_id}:input", {"data": "x"})


@pytest.mark.asyncio
async def test_shell_exit_marks_status_exited(
    kernel: KernelServer, caller: BusClient, terminal: TerminalPlugin
) -> None:
    term_id = (await caller.request("terminal:_:create", {"cwd": "/tmp", "shell": "/bin/bash"}))["id"]
    await caller.request(f"terminal:{term_id}:input", {"data": "exit 7\n"})

    status = await wait_for_status(kernel, term_id, "exited")
    assert status["exit_code"] == 7
