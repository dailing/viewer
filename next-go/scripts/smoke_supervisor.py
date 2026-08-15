#!/usr/bin/env python3
"""Black-box smoke test for the Go C0 supervisor plugin."""

from __future__ import annotations

import asyncio
import json
import os
import shlex
import signal
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))

from sdk import BusClient, RpcError  # noqa: E402

PORT = int(os.environ.get("VIEWER_SUPERVISOR_SMOKE_PORT", "29371"))
URL = f"ws://127.0.0.1:{PORT}/ws"
VIEWERD = Path(os.environ.get("VIEWERD_BIN", "/tmp/viewerd"))
SUPERVISOR = Path(os.environ.get("VIEWER_SUPERVISOR_BIN", "/tmp/viewer-supervisor"))
PYTHON = Path(os.environ.get("VIEWER_PYTHON", str(ROOT / "next" / ".venv" / "bin" / "python")))


def manifest(plugin_id: str) -> dict[str, Any]:
    return {"id": plugin_id, "version": "1.0.0", "slots": {}, "emits": {}}


async def wait_port(process: subprocess.Popen[Any]) -> None:
    for _ in range(100):
        if process.poll() is not None:
            raise AssertionError(f"kernel exited early: {process.returncode}")
        try:
            reader, writer = await asyncio.open_connection("127.0.0.1", PORT)
            del reader
            writer.close()
            await writer.wait_closed()
            return
        except OSError:
            await asyncio.sleep(0.05)
    raise AssertionError("kernel did not start")


def stop_process(process: subprocess.Popen[Any] | None) -> None:
    if process is None or process.poll() is not None:
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
        process.wait(timeout=4)
    except (ProcessLookupError, subprocess.TimeoutExpired):
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=4)


async def wait_state(queue: asyncio.Queue[dict[str, Any]], plugin_id: str, state: str, timeout: float = 22) -> dict[str, Any]:
    async with asyncio.timeout(timeout):
        while True:
            states = await queue.get()
            current = states.get(plugin_id, {})
            if current.get("state") == state:
                return current


def make_fake_plugins(base: Path) -> Path:
    worker = base / "worker.py"
    worker.write_text(
        """#!/usr/bin/env python3
import argparse, asyncio, os, signal, sys
sys.path.insert(0, os.environ['VIEWER_SMOKE_NEXT'])
from sdk import BusClient

async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('plugin_id')
    parser.add_argument('--kernel-ws', required=True)
    args = parser.parse_args()
    if args.plugin_id == 'crash-loop':
        raise SystemExit(7)
    await asyncio.sleep(0.3)
    client = BusClient(args.kernel_ws, {'id': args.plugin_id, 'version': '1.0.0', 'slots': {}, 'emits': {}}, managed=True, reconnect=False)
    await client.connect()
    await client.wait_registered()
    stopped = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, stopped.set)
    await stopped.wait()
    await client.close()

asyncio.run(main())
"""
    )
    entries = []
    for plugin_id in ("fake-ok", "crash-loop"):
        plugin = base / plugin_id
        backend = plugin / "backend"
        backend.mkdir(parents=True)
        run = backend / "run"
        run.write_text(
            "#!/bin/sh\nVIEWER_SMOKE_NEXT="
            + shlex.quote(str(ROOT / "next-go" / "scripts"))
            + " exec "
            + shlex.quote(str(PYTHON))
            + " "
            + shlex.quote(str(worker))
            + " "
            + shlex.quote(plugin_id)
            + ' "$@"\n'
        )
        run.chmod(0o755)
        entries.append({"id": plugin_id, "path": str(plugin), "enabled": True})
    registry = base / "registry.json"
    registry.write_text(json.dumps({"plugins": entries}))
    return registry


async def main() -> int:
    if not all(path.is_file() for path in (VIEWERD, SUPERVISOR, PYTHON)):
        raise AssertionError("build /tmp/viewerd and /tmp/viewer-supervisor first")
    kernel = supervisor = None
    clients: list[BusClient] = []
    with tempfile.TemporaryDirectory(prefix="viewer-supervisor-smoke-") as raw:
        temp = Path(raw)
        registry = make_fake_plugins(temp)
        kernel_log = (temp / "kernel.log").open("w+")
        supervisor_log = (temp / "supervisor.log").open("w+")
        try:
            kernel = subprocess.Popen(
                [str(VIEWERD), "--plugins=none", "--kernel-port", str(PORT), "--data-dir", str(temp / "data")],
                stdout=kernel_log, stderr=subprocess.STDOUT, start_new_session=True,
            )
            await wait_port(kernel)
            observer = BusClient(URL, manifest("supervisor-smoke-observer"), reconnect=False)
            states_queue: asyncio.Queue[dict[str, Any]] = asyncio.Queue()

            async def states_handler(frame: dict[str, Any]) -> None:
                await states_queue.put(frame["value"])

            await observer.subscribe("supervisor:_:states", states_handler)
            await observer.connect()
            await observer.wait_registered()
            clients.append(observer)
            supervisor = subprocess.Popen(
                [str(SUPERVISOR), "--kernel-ws", URL, "--registry", str(registry), "--log-dir", str(temp / "logs")],
                stdout=supervisor_log, stderr=subprocess.STDOUT, start_new_session=True,
            )

            starting = await wait_state(states_queue, "fake-ok", "starting")
            assert starting["pid"]
            running = await wait_state(states_queue, "fake-ok", "running")
            first_pid = running["pid"]
            print("PASS starting -> running")

            os.kill(first_pid, signal.SIGKILL)
            crashed = await wait_state(states_queue, "fake-ok", "crashed")
            assert crashed["exit_code"] is not None and crashed["crashes"] == 1
            restarted = await wait_state(states_queue, "fake-ok", "running")
            assert restarted["pid"] != first_pid and restarted["crashes"] == 0
            print("PASS crashed -> automatic restart -> running")

            broken = await wait_state(states_queue, "crash-loop", "broken", timeout=25)
            assert broken["crashes"] == 5 and broken["exit_code"] == 7
            print("PASS crash-loop circuit breaker")

            reply = await observer.request("supervisor:_:restart", {"id": "fake-ok"}, timeout=5)
            assert reply["id"] == "fake-ok" and reply["pid"] != restarted["pid"]
            manual = await wait_state(states_queue, "fake-ok", "running")
            assert manual["pid"] == reply["pid"] and manual["crashes"] == 0
            try:
                await observer.request("supervisor:_:restart", {"id": "missing"}, timeout=3)
            except RpcError as exc:
                assert exc.code == "not_found"
            else:
                raise AssertionError("missing restart did not return not_found")
            print("PASS restart RPC and not_found error")

            retained = BusClient(URL, manifest("supervisor-retained-reader"), reconnect=False)
            retained_queue: asyncio.Queue[dict[str, Any]] = asyncio.Queue()

            async def retained_handler(frame: dict[str, Any]) -> None:
                await retained_queue.put(frame["value"])

            await retained.subscribe("supervisor:_:states", retained_handler)
            await retained.connect()
            clients.append(retained)
            snapshot = await asyncio.wait_for(retained_queue.get(), 3)
            assert snapshot["fake-ok"]["state"] == "running" and snapshot["crash-loop"]["state"] == "broken"
            print("PASS retained states mailbox")
            print("RESULT 5/5 passed")
            return 0
        except Exception:
            for name, handle in (("kernel", kernel_log), ("supervisor", supervisor_log)):
                handle.flush(); handle.seek(0)
                print(f"--- {name} log tail ---", file=sys.stderr)
                print(handle.read()[-6000:], file=sys.stderr)
            raise
        finally:
            await asyncio.gather(*(client.close() for client in clients), return_exceptions=True)
            stop_process(supervisor)
            stop_process(kernel)
            kernel_log.close(); supervisor_log.close()


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
