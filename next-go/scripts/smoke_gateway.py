#!/usr/bin/env python3
"""Black-box smoke test for the Go C4 HTTP gateway."""

from __future__ import annotations

import argparse
import asyncio
import http.client
import json
import os
import signal
import sys
import uuid
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "next"))

import websockets  # noqa: E402
from websockets.exceptions import ConnectionClosed  # noqa: E402

from sdk import BusClient, Plugin, slot  # noqa: E402
from sdk.plugin import Ctx  # noqa: E402


def manifest(plugin_id: str) -> dict[str, Any]:
    return {"id": plugin_id, "version": "1.0.0", "slots": {}, "emits": {}}


class TerminalProbe(Plugin):
    manifest = {
        "id": "terminal",
        "version": "smoke",
        "slots": {"terminal:_:list": {"summary": "smoke RPC"}},
        "emits": {},
    }

    def __init__(self) -> None:
        super().__init__(reconnect=False)
        self.request_origin: dict[str, Any] | None = None

    @slot("terminal:_:list")
    async def list_terminals(self, ctx: Ctx) -> None:
        self.request_origin = ctx.origin
        await ctx.respond([{"id": "smoke", "state": "running"}])


async def wait_for_gateway_entry(client: BusClient) -> dict[str, Any]:
    found: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()

    async def inspect(frame: dict[str, Any]) -> None:
        for entry in frame.get("value") or []:
            if entry.get("conn") == client.conn and not found.done():
                found.set_result(entry)

    await client.subscribe("plugins:_:list", inspect)
    return await asyncio.wait_for(found, 3)


def http_get(base_url: str, path: str) -> tuple[int, dict[str, str], bytes]:
    parsed = urlsplit(base_url)
    connection = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=3)
    try:
        connection.request("GET", path)
        response = connection.getresponse()
        return response.status, dict(response.getheaders()), response.read()
    finally:
        connection.close()


async def run(args: argparse.Namespace) -> None:
    static_file = Path(args.static_dir) / "gateway-smoke.js"
    static_file.write_text("console.log('gateway smoke');\n", encoding="utf-8")

    probe = TerminalProbe()
    browser = BusClient(args.gateway_ws, manifest("browser-smoke"), reconnect=False)
    raw_browser = None
    try:
        await probe.start(args.kernel_ws, managed=False)
        await browser.connect()
        entry = await wait_for_gateway_entry(browser)
        assert entry["id"] == "gateway", entry
        assert entry["instance_id"] == browser.conn, entry
        assert entry["managed"] is False, entry
        print("PASS gateway registry entry uses browser conn and origin identity")

        result = await browser.request("terminal:_:list", timeout=3)
        assert result == [{"id": "smoke", "state": "running"}], result
        assert probe.request_origin == {
            "plugin": "gateway",
            "instance": browser.conn,
        }, probe.request_origin
        print("PASS RPC roundtrip preserves browser inbox routing and gateway origin")

        status, headers, body = await asyncio.to_thread(
            http_get, args.http_base, "/gateway-smoke.js"
        )
        assert status == 200, (status, body)
        assert headers.get("Content-Type", "").startswith("text/javascript"), headers
        assert body == static_file.read_bytes(), body
        print("PASS static GET 200 with JavaScript MIME")

        status, _, _ = await asyncio.to_thread(
            http_get, args.http_base, "/%2e%2e/%2e%2e/etc/passwd"
        )
        assert status == 404, status
        print("PASS traversal request returns 404")

        shutdown_conn = str(uuid.uuid4())
        raw_browser = await websockets.connect(args.gateway_ws)
        await raw_browser.send(
            json.dumps(
                {"type": "hello", "protocol_version": 1, "conn": shutdown_conn},
                separators=(",", ":"),
            )
        )
        await raw_browser.send(
            json.dumps({"type": "subscribe", "pattern": "plugins:_:list"})
        )
        async with asyncio.timeout(3):
            while True:
                frame = json.loads(await raw_browser.recv())
                if any(
                    item.get("conn") == shutdown_conn
                    for item in frame.get("value") or []
                ):
                    break
        os.kill(args.kernel_pid, signal.SIGTERM)
        try:
            await asyncio.wait_for(raw_browser.recv(), 5)
        except ConnectionClosed as closed:
            assert closed.code == 4009, (closed.code, closed.reason)
        else:
            raise AssertionError("browser remained open after kernel shutdown")
        print("PASS kernel close 4009 propagated to browser")

        unavailable = await websockets.connect(args.gateway_ws)
        try:
            await unavailable.send(
                json.dumps(
                    {
                        "type": "hello",
                        "protocol_version": 1,
                        "conn": str(uuid.uuid4()),
                    },
                    separators=(",", ":"),
                )
            )
            try:
                await asyncio.wait_for(unavailable.recv(), 3)
            except ConnectionClosed as closed:
                assert closed.code == 1012, (closed.code, closed.reason)
            else:
                raise AssertionError("gateway accepted browser while kernel was unavailable")
        finally:
            await unavailable.close()
        print("PASS unavailable kernel closes new browser with 1012")
    finally:
        if raw_browser is not None:
            await raw_browser.close()
        await browser.close()
        await probe.stop()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--kernel-ws", default="ws://127.0.0.1:29200/ws")
    parser.add_argument("--gateway-ws", default="ws://127.0.0.1:29201/ws")
    parser.add_argument("--http-base", default="http://127.0.0.1:29201")
    parser.add_argument("--static-dir", required=True)
    parser.add_argument("--kernel-pid", required=True, type=int)
    return parser.parse_args()


if __name__ == "__main__":
    asyncio.run(run(parse_args()))
