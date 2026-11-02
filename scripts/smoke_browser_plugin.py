#!/usr/bin/env python3
"""Black-box smoke test for the managed-Chrome viewer.browser plugin.

Uses real Chrome/CDP pages and real FFmpeg/Gateway stream setup. WebRTC packet
transport itself is covered by internal/plugins/gateway/media_test.go.
"""

import argparse
import asyncio
import json
import urllib.request
import uuid

import websockets


class Bus:
    def __init__(self, url):
        self.url = url
        self.conn = str(uuid.uuid4())
        self.ws = None
        self.reader_task = None
        self.pending = {}

    async def __aenter__(self):
        self.ws = await websockets.connect(self.url, max_size=None)
        await self.ws.send(json.dumps({"type": "hello", "protocol_version": 1, "conn": self.conn}))
        self.reader_task = asyncio.create_task(self._reader())
        return self

    async def __aexit__(self, *_):
        if self.reader_task:
            self.reader_task.cancel()
        if self.ws:
            await self.ws.close()

    async def _reader(self):
        async for raw in self.ws:
            value = (json.loads(raw).get("value") or {})
            corr = value.get("_corr")
            if corr in self.pending:
                future = self.pending.pop(corr)
                if not future.done():
                    future.set_result(value)

    async def request(self, channel, payload=None, timeout=60):
        assert self.ws is not None
        corr = uuid.uuid4().hex
        inbox = f"_inbox:{self.conn}:{corr}"
        future = asyncio.get_running_loop().create_future()
        self.pending[corr] = future
        await self.ws.send(json.dumps({"type": "subscribe", "pattern": inbox}))
        await self.ws.send(json.dumps({
            "type": "publish", "channel": channel,
            "value": {**(payload or {}), "_reply_to": inbox, "_corr": corr},
        }))
        response = await asyncio.wait_for(future, timeout)
        await self.ws.send(json.dumps({"type": "unsubscribe", "pattern": inbox}))
        if not response.get("ok"):
            error = response.get("error") or {}
            raise RuntimeError(f"{error.get('code')}: {error.get('message')}")
        return response.get("result")


def mcp_request(url, method, params):
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode()
    request = urllib.request.Request(url, data=body, headers={
        "Content-Type": "application/json", "Accept": "application/json, text/event-stream",
    })
    with urllib.request.urlopen(request, timeout=45) as response:
        payload = json.load(response)
    if "error" in payload:
        error = payload["error"]
        raise RuntimeError(f"{error.get('code')}: {error.get('message')}")
    result = payload["result"]
    if method == "tools/call" and result.get("isError"):
        error = (result.get("structuredContent") or {}).get("error") or {}
        raise RuntimeError(f"{error.get('code')}: {error.get('message')}")
    return result


async def mcp(url, method, params):
    return await asyncio.to_thread(mcp_request, url, method, params)


async def expect_error(code, coroutine):
    try:
        await coroutine
    except RuntimeError as error:
        if str(error).startswith(code + ":"):
            return
        raise AssertionError(f"expected {code}, got {error}") from error
    raise AssertionError(f"expected {code}, but call succeeded")


async def run(url, mcp_url):
    page_ids = []
    async with Bus(url) as bus:
        initialized = await mcp(mcp_url, "initialize", {"protocolVersion": "2025-03-26"})
        assert initialized["serverInfo"]["name"] == "viewer-browser"
        tools = await mcp(mcp_url, "tools/list", {})
        names = {tool["name"] for tool in tools["tools"]}
        assert "browser_list_targets" not in names and "browser_bind" not in names
        try:
            page_a = await bus.request("viewer.browser:_:create", {"url": "https://example.com"})
            page_b = await bus.request("viewer.browser:_:create", {"url": "https://example.org"})
            page_ids = [page_a["page_id"], page_b["page_id"]]
            id_a, id_b = page_ids
            assert id_a != id_b and id_a.startswith("pg_") and len(id_a) == 29

            lease_a = (await mcp(mcp_url, "tools/call", {
                "name": "browser_require", "arguments": {"page_id": id_a},
            }))["structuredContent"]["lease_id"]
            await expect_error("page_busy", mcp(mcp_url, "tools/call", {
                "name": "browser_require", "arguments": {"page_id": id_a},
            }))
            lease_b = (await mcp(mcp_url, "tools/call", {
                "name": "browser_require", "arguments": {"page_id": id_b},
            }))["structuredContent"]["lease_id"]
            await expect_error("invalid_lease", mcp(mcp_url, "tools/call", {
                "name": "browser_snapshot", "arguments": {"page_id": id_a, "lease_id": lease_b},
            }))
            snapshot = (await mcp(mcp_url, "tools/call", {
                "name": "browser_snapshot", "arguments": {"page_id": id_a, "lease_id": lease_a},
            }))["structuredContent"]
            assert snapshot["title"] == "Example Domain" and snapshot["elements"]
            clicked = (await mcp(mcp_url, "tools/call", {
                "name": "browser_click", "arguments": {
                    "page_id": id_a, "lease_id": lease_a,
                    "snapshot_id": snapshot["snapshot_id"], "element_id": snapshot["elements"][0]["id"],
                },
            }))["structuredContent"]
            assert clicked["clicked"] is True

            watched = await bus.request("viewer.browser:_:watch", {
                "page_id": id_a, "viewer_id": "managed-smoke", "pane_width": 640,
                "pane_height": 360, "page_zoom": 1, "render_scale": 1.5,
                "fps": 5, "bitrate_kbps": 500,
            })
            page, stream = watched["page"], watched["stream"]
            assert page["viewport"]["css_width"] == 640
            assert page["viewport"]["pixel_width"] == 960
            assert page["video"] == {"fps": 5, "bitrate_kbps": 500}
            assert stream["stream_id"] and stream["ticket"]
            rewatched = await bus.request("viewer.browser:_:watch", {
                "page_id": id_a, "viewer_id": "managed-smoke", "pane_width": 640,
                "pane_height": 360, "page_zoom": 1, "render_scale": 1.5,
                "fps": 5, "bitrate_kbps": 1000,
            })
            assert rewatched["page"]["viewport"]["pixel_width"] == 960
            assert rewatched["page"]["video"] == {"fps": 5, "bitrate_kbps": 1000}
            await bus.request("viewer.browser:_:unwatch", {"page_id": id_a, "viewer_id": "managed-smoke"})

            await mcp(mcp_url, "tools/call", {
                "name": "browser_release", "arguments": {"page_id": id_a, "lease_id": lease_a},
            })
            await expect_error("invalid_lease", mcp(mcp_url, "tools/call", {
                "name": "browser_snapshot", "arguments": {"page_id": id_a, "lease_id": lease_a},
            }))
            print("PASS managed Chrome MCP + page isolation + lease fencing + H.264 stream setup + independent video controls")
        finally:
            for page_id in page_ids:
                try:
                    await bus.request("viewer.browser:_:close", {"page_id": page_id})
                except RuntimeError:
                    pass


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--gateway", default="ws://127.0.0.1:18100/ws")
    parser.add_argument("--mcp", default="http://127.0.0.1:18732/mcp")
    args = parser.parse_args()
    asyncio.run(run(args.gateway, args.mcp))
