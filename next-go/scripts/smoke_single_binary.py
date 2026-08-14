#!/usr/bin/env python3
"""End-to-end smoke checks for the assembled single-binary viewerd."""

from __future__ import annotations

import argparse
import asyncio
import os
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path
from typing import Any, Callable

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "next"))

from sdk import BusClient  # noqa: E402


CORE_IDS = {
    "bus-inspector",
    "config-store",
    "viewer.agent-hermes",
    "instance-store",
    "file-service",
    "chat",
    "terminal",
    "supervisor",
    "gateway",
}


def manifest(plugin_id: str) -> dict[str, Any]:
    return {"id": plugin_id, "version": "1.0.0", "slots": {}, "emits": {}}


def free_port() -> int:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


async def wait_for(probe: Callable[[], Any], timeout: float = 10.0) -> Any:
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        value = probe()
        if value:
            return value
        await asyncio.sleep(0.03)
    raise TimeoutError("condition not met within timeout")


class Viewerd:
    def __init__(self, binary: Path, gateway_port: int, kernel_port: int, data_dir: Path) -> None:
        self.gateway_port = gateway_port
        self.kernel_port = kernel_port
        self.log_path = data_dir.parent / "viewerd.log"
        self.command = [
            str(binary),
            "--host",
            "127.0.0.1",
            "--port",
            str(gateway_port),
            "--kernel-host",
            "127.0.0.1",
            "--kernel-port",
            str(kernel_port),
            "--data-dir",
            str(data_dir),
        ]
        self.process: subprocess.Popen[bytes] | None = None

    def start(self) -> None:
        log = self.log_path.open("ab")
        try:
            self.process = subprocess.Popen(self.command, stdout=log, stderr=log)
        finally:
            log.close()

    def assert_running(self) -> None:
        if self.process is not None and self.process.poll() is not None:
            raise AssertionError(
                f"viewerd exited with {self.process.returncode}:\n{self.log_path.read_text(errors='replace')[-6000:]}"
            )

    async def wait_http(self) -> str:
        url = f"http://127.0.0.1:{self.gateway_port}/"
        deadline = asyncio.get_running_loop().time() + 10
        while asyncio.get_running_loop().time() < deadline:
            self.assert_running()
            try:
                return await asyncio.to_thread(
                    lambda: urllib.request.urlopen(url, timeout=0.5).read().decode("utf-8")
                )
            except OSError:
                await asyncio.sleep(0.05)
        raise TimeoutError("gateway did not become ready")

    async def terminate_cleanly(self) -> None:
        process = self.process
        assert process is not None
        process.send_signal(signal.SIGTERM)
        try:
            code = await asyncio.wait_for(asyncio.to_thread(process.wait), 10)
        except TimeoutError:
            process.kill()
            await asyncio.to_thread(process.wait)
            raise AssertionError("viewerd did not exit within 10 seconds")
        assert code == 0, self.log_path.read_text(errors="replace")[-6000:]


async def run(args: argparse.Namespace) -> None:
    with tempfile.TemporaryDirectory(prefix="viewer-single-binary-") as temporary:
        temp = Path(temporary)
        data_dir = temp / "data"
        gateway_port, kernel_port = free_port(), free_port()
        while kernel_port == gateway_port:
            kernel_port = free_port()
        daemon = Viewerd(Path(args.viewerd_bin), gateway_port, kernel_port, data_dir)
        daemon.start()
        clients: list[BusClient] = []
        try:
            index = await daemon.wait_http()
            assert "Viewer frontend has not been built" in index
            print("PASS embedded frontend index")

            gateway = BusClient(
                f"ws://127.0.0.1:{gateway_port}/ws",
                manifest("single-binary-gateway-client"),
                reconnect=False,
            )
            registry_ids: set[str] = set()
            registry_entries: dict[str, dict[str, Any]] = {}

            async def registry_handler(frame: dict[str, Any]) -> None:
                registry_ids.clear()
                registry_entries.clear()
                for entry in frame["value"]:
                    plugin_id = entry["manifest"]["id"]
                    registry_ids.add(plugin_id)
                    registry_entries[plugin_id] = entry

            await gateway.subscribe("plugins:_:list", registry_handler)
            await gateway.connect()
            clients.append(gateway)
            await wait_for(lambda: CORE_IDS <= registry_ids)
            assert CORE_IDS <= registry_ids, registry_ids
            assert all(
                registry_entries[plugin_id]["managed"] is False for plugin_id in CORE_IDS
            )
            print("PASS gateway /ws registry contains all 9 resident plugins")

            await gateway.request(
                "config:_:set",
                {"plugin": "m4-smoke", "key": "answer", "value": "persisted-value"},
            )
            assert await gateway.request(
                "config:_:get", {"plugin": "m4-smoke", "key": "answer"}
            ) == "persisted-value"
            print("PASS config set/get")

            await gateway.request(
                "instance:_:set",
                {"plugin": "m4-smoke", "instance": "one", "value": {"count": 4}},
            )
            assert await gateway.request(
                "instance:_:get", {"plugin": "m4-smoke", "instance": "one"}
            ) == {"count": 4}
            print("PASS instance set/get")

            outputs: dict[str, list[str]] = {}

            async def terminal_output(frame: dict[str, Any]) -> None:
                terminal_id = frame["channel"].split(":")[1]
                outputs.setdefault(terminal_id, []).append(frame["value"]["data"])

            await gateway.subscribe("terminal:*:output", terminal_output)
            created = await gateway.request("terminal:_:create")
            terminal_id = created["id"]
            outputs[terminal_id] = []
            marker = f"M4_TERMINAL_{time.time_ns()}"
            await gateway.request(
                f"terminal:{terminal_id}:write", {"data": f"echo {marker}\n"}
            )
            await wait_for(lambda: marker in "".join(outputs[terminal_id]))
            await gateway.request(f"terminal:{terminal_id}:kill")
            print("PASS terminal create/write/output/close")

            await asyncio.sleep(0.15)
            snapshot = await gateway.request(
                "bus-inspector:_:snapshot", {"limit": 1000}, timeout=5
            )
            channels = {entry["channel"] for entry in snapshot["entries"]}
            required_channels = {
                "config:_:set",
                "config:_:get",
                "instance:_:set",
                "instance:_:get",
                "terminal:_:create",
                f"terminal:{terminal_id}:write",
            }
            assert required_channels <= channels, required_channels - channels
            print("PASS inspector recorded assembled traffic")

            direct = BusClient(
                f"ws://127.0.0.1:{kernel_port}/ws",
                manifest("external-plugin-smoke"),
                reconnect=False,
            )
            direct_ids: set[str] = set()

            async def direct_registry(frame: dict[str, Any]) -> None:
                direct_ids.update(entry["manifest"]["id"] for entry in frame["value"])

            await direct.subscribe("plugins:_:list", direct_registry)
            await direct.connect()
            clients.append(direct)
            await wait_for(lambda: CORE_IDS <= direct_ids)
            assert await direct.request(
                "config:_:get", {"plugin": "m4-smoke", "key": "answer"}
            ) == "persisted-value"
            print("PASS external-style direct kernel client registry/RPC")

            await daemon.terminate_cleanly()
            print("PASS SIGTERM clean exit within 10s")
            await asyncio.gather(*(client.close() for client in clients), return_exceptions=True)
            clients.clear()

            daemon = Viewerd(Path(args.viewerd_bin), gateway_port, kernel_port, data_dir)
            daemon.start()
            await daemon.wait_http()
            persisted = BusClient(
                f"ws://127.0.0.1:{gateway_port}/ws",
                manifest("persistence-smoke"),
                reconnect=False,
            )
            await persisted.connect()
            clients.append(persisted)
            assert await persisted.request(
                "config:_:get", {"plugin": "m4-smoke", "key": "answer"}
            ) == "persisted-value"
            print("PASS restart preserves config value")
            await persisted.close()
            clients.clear()
            await daemon.terminate_cleanly()
            print("PASS single-binary smoke complete")
        except Exception:
            if daemon.log_path.exists():
                print("--- viewerd log tail ---", file=sys.stderr)
                print(daemon.log_path.read_text(errors="replace")[-6000:], file=sys.stderr)
            raise
        finally:
            await asyncio.gather(*(client.close() for client in clients), return_exceptions=True)
            if daemon.process is not None and daemon.process.poll() is None:
                daemon.process.terminate()
                try:
                    await asyncio.wait_for(asyncio.to_thread(daemon.process.wait), 3)
                except TimeoutError:
                    daemon.process.kill()
                    await asyncio.to_thread(daemon.process.wait)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--viewerd-bin",
        default=os.environ.get("VIEWERD_BIN", "/tmp/viewerd"),
    )
    asyncio.run(run(parser.parse_args()))


if __name__ == "__main__":
    main()
