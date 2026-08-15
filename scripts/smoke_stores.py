#!/usr/bin/env python3
"""Black-box smoke checks for the Go config-store and instance-store."""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from sdk import BusClient, RpcError  # noqa: E402


def manifest(plugin_id: str) -> dict[str, Any]:
    return {"id": plugin_id, "version": "1.0.0", "slots": {}, "emits": {}}


class PluginProcess:
    def __init__(self, binary: str, kernel_ws: str, db_path: Path, log_path: Path) -> None:
        self.command = [binary, "--kernel-ws", kernel_ws, "--db", str(db_path)]
        self.log_path = log_path
        self.process: subprocess.Popen[bytes] | None = None

    def start(self) -> None:
        log = self.log_path.open("ab")
        try:
            self.process = subprocess.Popen(self.command, stdout=log, stderr=log)
        finally:
            log.close()

    async def stop(self) -> None:
        process = self.process
        if process is None or process.poll() is not None:
            return
        process.terminate()
        try:
            await asyncio.to_thread(process.wait, 3)
        except subprocess.TimeoutExpired:
            process.kill()
            await asyncio.to_thread(process.wait)

    def assert_running(self) -> None:
        if self.process is not None and self.process.poll() is not None:
            tail = self.log_path.read_text(errors="replace")[-4000:]
            raise AssertionError(f"plugin exited with {self.process.returncode}:\n{tail}")


class Registry:
    def __init__(self) -> None:
        self.ids: set[str] = set()
        self.changed = asyncio.Event()

    async def handler(self, frame: dict[str, Any]) -> None:
        self.ids = {entry["manifest"]["id"] for entry in frame["value"]}
        self.changed.set()

    async def wait(self, plugin_id: str, present: bool, process: PluginProcess) -> None:
        deadline = asyncio.get_running_loop().time() + 5
        while (plugin_id in self.ids) is not present:
            if present:
                process.assert_running()
            remaining = deadline - asyncio.get_running_loop().time()
            if remaining <= 0:
                raise TimeoutError(f"registry did not reach {plugin_id} present={present}")
            self.changed.clear()
            try:
                await asyncio.wait_for(self.changed.wait(), min(remaining, 0.25))
            except TimeoutError:
                pass


async def expect_error(client: BusClient, channel: str, value: Any, code: str) -> None:
    try:
        await client.request(channel, value, timeout=2)
    except RpcError as exc:
        assert exc.code == code, (channel, exc.code, exc.message)
    else:
        raise AssertionError(f"{channel} unexpectedly succeeded")


async def retained(client: BusClient, channel: str) -> Any:
    loop = asyncio.get_running_loop()
    seen: asyncio.Future[Any] = loop.create_future()

    async def handler(frame: dict[str, Any]) -> None:
        if not seen.done():
            seen.set_result(frame["value"])

    await client.subscribe(channel, handler)
    try:
        return await asyncio.wait_for(seen, 2)
    finally:
        await client.unsubscribe(channel, handler)


async def run(args: argparse.Namespace) -> None:
    with tempfile.TemporaryDirectory(prefix="viewer-stores-") as temporary:
        temp = Path(temporary)
        config_db = temp / "config.json"
        instance_db = temp / "instance.json"
        config = PluginProcess(args.configstore_bin, args.kernel_ws, config_db, temp / "config.log")
        instance = PluginProcess(
            args.instancestore_bin, args.kernel_ws, instance_db, temp / "instance.log"
        )
        client = BusClient(args.kernel_ws, manifest("smoke-stores"), reconnect=False)
        registry = Registry()
        await client.subscribe("plugins:_:list", registry.handler)
        await client.connect()
        await client.wait_registered()
        try:
            config.start()
            instance.start()
            await registry.wait("config-store", True, config)
            await registry.wait("instance-store", True, instance)

            await expect_error(client, "config:_:get", {}, "invalid_request")
            await expect_error(client, "config:_:set", {"plugin": "chat"}, "invalid_request")
            result = await client.request(
                "config:_:set", {"plugin": "chat", "key": "roles", "value": ["a", "b"]}
            )
            assert result == {"plugin": "chat", "key": "roles", "value": ["a", "b"]}
            await client.request(
                "config:_:set", {"plugin": "chat", "key": "theme", "value": "dark"}
            )
            assert await client.request("config:_:get", {"plugin": "chat", "key": "roles"}) == [
                "a",
                "b",
            ]
            assert await client.request("config:_:get", {"plugin": "chat"}) == {
                "roles": ["a", "b"],
                "theme": "dark",
            }
            assert await client.request("config:_:list", {}) == {
                "chat": {"roles": ["a", "b"], "theme": "dark"}
            }
            await client.request(
                "config:_:set", {"plugin": "chat", "key": "roles", "value": None}
            )
            assert await retained(client, "config:chat:config") == {"theme": "dark"}
            assert json.loads(config_db.read_text()) == {"chat": {"theme": "dark"}}
            assert not Path(str(config_db) + ".tmp").exists()
            print("config-store CRUD/errors/atomic mailbox: PASS")

            for channel, payload in (
                ("instance:_:get", {}),
                ("instance:_:set", {"plugin": "chat"}),
                ("instance:_:delete", {"plugin": "chat"}),
                ("instance:_:list", {}),
            ):
                await expect_error(client, channel, payload, "invalid_request")
            await expect_error(
                client,
                "instance:_:set",
                {"plugin": "chat", "instance": "room-1", "value": "bad"},
                "invalid_request",
            )
            replaced = await client.request(
                "instance:_:set",
                {"plugin": "chat", "instance": "room-1", "value": {"count": 1, "cwd": "/tmp"}},
            )
            assert replaced["state"] == {"count": 1, "cwd": "/tmp"}
            assert await client.request(
                "instance:_:get", {"plugin": "chat", "instance": "room-1", "key": "count"}
            ) == 1
            await client.request(
                "instance:_:set",
                {"plugin": "chat", "instance": "room-1", "key": "tags", "value": ["x"]},
            )
            await client.request(
                "instance:_:set",
                {"plugin": "chat", "instance": "room-1", "key": "cwd", "value": None},
            )
            expected_state = {"count": 1, "tags": ["x"]}
            assert await client.request(
                "instance:_:get", {"plugin": "chat", "instance": "room-1"}
            ) == expected_state
            assert await client.request("instance:_:list", {"plugin": "chat"}) == {
                "room-1": expected_state
            }
            assert await retained(client, "instance-store:chat:room-1") == expected_state
            deleted = await client.request(
                "instance:_:delete", {"plugin": "chat", "instance": "room-1"}
            )
            assert deleted["existed"] is True
            assert await retained(client, "instance-store:chat:room-1") is None
            await client.request(
                "instance:_:set",
                {"plugin": "chat", "instance": "persisted", "value": {"alive": True}},
            )
            assert json.loads(instance_db.read_text()) == {
                "chat": {"persisted": {"alive": True}}
            }
            assert not Path(str(instance_db) + ".tmp").exists()
            print("instance-store CRUD/errors/mailbox tombstone: PASS")

            await config.stop()
            await instance.stop()
            await registry.wait("config-store", False, config)
            await registry.wait("instance-store", False, instance)
            config.start()
            instance.start()
            await registry.wait("config-store", True, config)
            await registry.wait("instance-store", True, instance)
            assert await client.request("config:_:get", {"plugin": "chat"}) == {"theme": "dark"}
            assert await client.request(
                "instance:_:get", {"plugin": "chat", "instance": "persisted"}
            ) == {"alive": True}
            print("store process reopen persistence: PASS")
        finally:
            await config.stop()
            await instance.stop()
            await client.close()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--kernel-ws", default=os.environ.get("VIEWER_KERNEL_WS", "ws://127.0.0.1:29430/ws")
    )
    parser.add_argument(
        "--configstore-bin", default=os.environ.get("VIEWER_CONFIGSTORE_BIN", "/tmp/viewer-configstore")
    )
    parser.add_argument(
        "--instancestore-bin",
        default=os.environ.get("VIEWER_INSTANCESTORE_BIN", "/tmp/viewer-instancestore"),
    )
    asyncio.run(run(parser.parse_args()))


if __name__ == "__main__":
    main()
