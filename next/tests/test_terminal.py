"""viewer.terminal tests against a real kernel + real PTYs."""

from __future__ import annotations

import asyncio
import json

import pytest

from kernel.server import KernelServer
from plugins.terminal import TerminalPlugin
from plugins.terminal.terminal import FLUSH_CHARS, SNAPSHOT_BUDGET
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


@pytest.mark.asyncio
async def test_output_coalesces_rapid_bursts(
    caller: BusClient, terminal: TerminalPlugin
) -> None:
    """A fast multi-read burst lands in the ring as coalesced frames, not one
    frame per read — a merged entry can exceed the single-read size."""

    term_id = (await caller.request("terminal:_:create", {"cwd": "/tmp", "shell": "/bin/bash"}))["id"]
    await caller.request(
        f"terminal:{term_id}:input", {"data": "yes x | head -c 200000\n"}
    )

    def ring_text() -> str:
        session = terminal.sessions.get(term_id)
        if session is None:
            return ""
        return "".join(entry["data"] for entry in session.ring)

    # Wait for the burst itself (the command echo alone contains just a
    # handful of x's — waiting on a marker string would match the echo).
    await wait_for(lambda: ring_text().count("x") >= 99_000, timeout=10.0)
    session = terminal.sessions[term_id]
    burst_entries = [entry for entry in session.ring if "x" in entry["data"]]
    assert burst_entries, "expected ring entries for the burst"
    assert max(len(entry["data"]) for entry in burst_entries) > 65536, (
        "no entry larger than a single read — coalescing did not happen"
    )
    assert max(len(entry["data"]) for entry in burst_entries) <= FLUSH_CHARS


@pytest.mark.asyncio
async def test_snapshot_response_stays_under_kernel_frame_limit(
    caller: BusClient, terminal: TerminalPlugin
) -> None:
    """Regression: a ring full of large chunks made the snapshot RPC reply
    exceed the kernel's 1 MiB frame limit — the kernel rejected the publish
    and the caller hung until timeout (reopen-a-terminal broke). The reply
    is now byte-budgeted and always returns."""

    term_id = (await caller.request("terminal:_:create", {"cwd": "/tmp", "shell": "/bin/bash"}))["id"]
    await caller.request(
        f"terminal:{term_id}:input", {"data": "yes y | head -c 3000000\n"}
    )

    session = terminal.sessions[term_id]

    def ring_text() -> str:
        return "".join(entry["data"] for entry in session.ring)

    # 3 MB of "y\n" = 1.5 M y's; the command echo has only a handful.
    await wait_for(lambda: ring_text().count("y") >= 1_499_000, timeout=15.0)
    await asyncio.sleep(0.1)  # let the final flush land

    snapshot = await caller.request(f"terminal:{term_id}:snapshot", {"limit": 500}, timeout=10.0)
    entries = snapshot["entries"]
    assert entries, "snapshot must return at least the newest entry"
    assert "".join(entry["data"] for entry in entries).count("y") > 100_000
    assert len(json.dumps(snapshot)) < SNAPSHOT_BUDGET + 8192
    # The budget trimmed older history: fewer entries than the full ring.
    assert len(entries) < len(session.ring)
    # Seq continuity is preserved for the returned tail.
    assert [entry["seq"] for entry in entries] == sorted(entry["seq"] for entry in entries)
