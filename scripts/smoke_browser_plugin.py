#!/usr/bin/env python3
"""Black-box smoke test for the managed-Chrome viewer.browser plugin.

Uses real Chrome/CDP pages and real FFmpeg/Gateway stream setup. WebRTC packet
transport itself is covered by internal/plugins/gateway/media_test.go.
"""

import argparse
import asyncio
import http.server
import json
import os
import pathlib
import threading
import urllib.request
import uuid

import websockets


class AgentFixture(http.server.BaseHTTPRequestHandler):
    authenticated = False

    def do_GET(self):
        if self.path == "/human-complete":
            type(self).authenticated = True
            body = "Test identity provider: authentication complete"
        elif self.path == "/login":
            if type(self).authenticated:
                body = '<title>Account ready</title><h1>Account ready</h1><a href="/account">Open account</a>'
            else:
                body = '<title>Fixture login</title><h1>Login required</h1><input placeholder="Account name"><input type="password" placeholder="Password"><button>Sign in</button>'
        elif self.path == "/account":
            body = '<title>Account home</title><h1>Account home</h1><p>Authenticated fixture content</p>' if type(self).authenticated else "Login required"
        elif self.path == "/one":
            body = '<title>Human tab one</title><h1>Human tab one</h1><button>One action</button>'
        elif self.path == "/two":
            body = '<title>Human tab two</title><h1>Human tab two</h1><button>Two action</button>'
        else:
            self.send_error(404)
            return
        encoded = body.encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def log_message(self, format: str, *args: object) -> None:
        pass


def start_agent_fixture():
    AgentFixture.authenticated = False
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), AgentFixture)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server


async def tool(url, name, arguments):
    result = await mcp(url, "tools/call", {"name": name, "arguments": arguments})
    return result["structuredContent"]


def element(snapshot, text):
    return next(item for item in snapshot["elements"] if text in item.get("text", ""))


async def human_activate_tab(url):
    """Test-only human tab selection; no Agent MCP tool performs this action."""
    profile = pathlib.Path(os.environ.get("VIEWER_BROWSER_PROFILE", str(pathlib.Path.home() / ".local/share/viewer/browser-profile")))
    port = profile.joinpath("DevToolsActivePort").read_text().splitlines()[0]
    base = f"http://127.0.0.1:{port}"

    def debug_json(path):
        with urllib.request.urlopen(base + path, timeout=5) as response:
            return json.load(response)

    targets, version = await asyncio.gather(asyncio.to_thread(debug_json, "/json/list"), asyncio.to_thread(debug_json, "/json/version"))
    target = next(item for item in targets if item.get("type") == "page" and item.get("url") == url)
    async with websockets.connect(version["webSocketDebuggerUrl"]) as ws:
        await ws.send(json.dumps({"id": 1, "method": "Target.activateTarget", "params": {"targetId": target["id"]}}))
        while True:
            response = json.loads(await ws.recv())
            if response.get("id") == 1:
                assert "error" not in response, response
                break
    await asyncio.sleep(0.3)


async def run_agent_recovery_flows(bus, mcp_url, fixture_url, page_ids):
    login = await bus.request("viewer.browser:_:create", {"url": "about:blank"})
    login_id = login["page_id"]
    page_ids.append(login_id)
    first_lease = (await tool(mcp_url, "browser_require", {"page_id": login_id}))["lease_id"]
    await tool(mcp_url, "browser_navigate", {"page_id": login_id, "lease_id": first_lease, "url": fixture_url + "/login"})
    login_snapshot = await tool(mcp_url, "browser_snapshot", {"page_id": login_id, "lease_id": first_lease})
    assert any(item.get("input_type") == "password" for item in login_snapshot["elements"])
    # Agent Turn 1 stops before credentials and releases. The test identity
    # provider, not the Agent or CDP, completes authentication between Turns.
    await tool(mcp_url, "browser_release", {"page_id": login_id, "lease_id": first_lease})
    await asyncio.to_thread(lambda: urllib.request.urlopen(fixture_url + "/human-complete", timeout=5).read())
    resumed_lease = (await tool(mcp_url, "browser_require", {"page_id": login_id}))["lease_id"]
    assert resumed_lease != first_lease
    await expect_error("invalid_lease", tool(mcp_url, "browser_snapshot", {"page_id": login_id, "lease_id": first_lease}))
    await tool(mcp_url, "browser_reload", {"page_id": login_id, "lease_id": resumed_lease})
    ready = await tool(mcp_url, "browser_snapshot", {"page_id": login_id, "lease_id": resumed_lease})
    assert "Account ready" in ready["text"] and not any(item.get("input_type") == "password" for item in ready["elements"])
    await tool(mcp_url, "browser_click", {"page_id": login_id, "lease_id": resumed_lease, "snapshot_id": ready["snapshot_id"], "element_id": element(ready, "Open account")["id"]})
    await tool(mcp_url, "browser_wait", {"page_id": login_id, "lease_id": resumed_lease, "text": "Account home"})
    await tool(mcp_url, "browser_release", {"page_id": login_id, "lease_id": resumed_lease})
    print(f"PASS login handoff: page={login_id} old_lease=invalid fresh_snapshot=Account home")

    tabs = await bus.request("viewer.browser:_:create", {"url": fixture_url + "/one"})
    tabs_id = tabs["page_id"]
    page_ids.append(tabs_id)
    agent_lease = (await tool(mcp_url, "browser_require", {"page_id": tabs_id}))["lease_id"]
    opened = await tool(mcp_url, "browser_new_tab", {"page_id": tabs_id, "lease_id": agent_lease, "url": fixture_url + "/two"})
    agent_tab = opened["tab_id"]
    await bus.request("viewer.browser:_:watch", {"page_id": tabs_id, "viewer_id": "human-tab-smoke", "pane_width": 800, "pane_height": 600, "page_zoom": 1, "render_scale": 1, "fps": 5, "bitrate_kbps": 500})
    await bus.request("viewer.browser:_:control", {"page_id": tabs_id, "viewer_id": "human-tab-smoke", "takeover": True})
    await expect_error("invalid_lease", tool(mcp_url, "browser_list_tabs", {"page_id": tabs_id, "lease_id": agent_lease}))
    await human_activate_tab(fixture_url + "/one")
    await bus.request("viewer.browser:_:release", {"page_id": tabs_id})
    await bus.request("viewer.browser:_:unwatch", {"page_id": tabs_id, "viewer_id": "human-tab-smoke"})
    resumed_lease = (await tool(mcp_url, "browser_require", {"page_id": tabs_id}))["lease_id"]
    listed = await tool(mcp_url, "browser_list_tabs", {"page_id": tabs_id, "lease_id": resumed_lease})
    human_tab = next(item["tab_id"] for item in listed["tabs"] if item["active"])
    assert human_tab != agent_tab, f"human tab activation did not change active tab: {listed}"
    # Deliberately do not call browser_switch_tab. A resumed Agent observes the
    # human-selected tab with fresh references and leaves that selection intact.
    current = await tool(mcp_url, "browser_snapshot", {"page_id": tabs_id, "lease_id": resumed_lease})
    assert current["tab_id"] == human_tab and "Human tab one" in current["text"]
    listed_again = await tool(mcp_url, "browser_list_tabs", {"page_id": tabs_id, "lease_id": resumed_lease})
    assert next(item["tab_id"] for item in listed_again["tabs"] if item["active"]) == human_tab
    await tool(mcp_url, "browser_release", {"page_id": tabs_id, "lease_id": resumed_lease})
    print(f"PASS human active-tab resume: page={tabs_id} agent_tab={agent_tab} human_tab={human_tab} no_switch_back=true")


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
    fixture = start_agent_fixture()
    fixture_url = f"http://127.0.0.1:{fixture.server_address[1]}"
    async with Bus(url) as bus:
        initialized = await mcp(mcp_url, "initialize", {"protocolVersion": "2025-03-26"})
        assert initialized["serverInfo"]["name"] == "viewer-browser"
        assert initialized["serverInfo"]["version"] == "0.3.0"
        tools = await mcp(mcp_url, "tools/list", {})
        names = {tool["name"] for tool in tools["tools"]}
        assert "browser_list_targets" not in names and "browser_bind" not in names
        expected_tools = {
            "browser_evaluate", "browser_screenshot", "browser_upload", "browser_downloads",
            "browser_list_tabs", "browser_new_tab", "browser_switch_tab", "browser_close_tab",
            "browser_double_click", "browser_right_click", "browser_hover", "browser_type",
            "browser_select", "browser_send_keys", "browser_find_text", "browser_dropdown_options",
        }
        assert expected_tools <= names, f"missing tools: {sorted(expected_tools - names)}"
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
            await run_agent_recovery_flows(bus, mcp_url, fixture_url, page_ids)
            print("PASS managed Chrome MCP + page isolation + lease fencing + H.264 stream setup + independent video controls")
        finally:
            for page_id in page_ids:
                try:
                    await bus.request("viewer.browser:_:close", {"page_id": page_id})
                except RuntimeError:
                    pass
            fixture.shutdown()
            fixture.server_close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--gateway", default="ws://127.0.0.1:18100/ws")
    parser.add_argument("--mcp", default="http://127.0.0.1:18732/mcp")
    args = parser.parse_args()
    asyncio.run(run(args.gateway, args.mcp))
