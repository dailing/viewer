"""C4: viewer.http-gateway — single-WS browser translator + static assets.

Browser connects to the gateway's `/ws` speaking the same 5-frame protocol
(protocol spec section 11). For each browser connection the gateway opens a
dedicated kernel connection and pipes frames verbatim in both directions; the
kernel-side hello carries the *browser's* conn id with the gateway manifest,
so browser-originated traffic carries origin plugin `gateway` and RPC inboxes
(`_inbox:{browser_conn}:{corr}`) route unchanged. Per-browser kernel
connections (rather than one multiplexed connection) are deliberate: mailbox
retained replay on subscribe then works per browser with zero gateway-side
subscription bookkeeping. Static frontend assets are served on the same port
via the websockets HTTP hook. By-reference large-file data plane: later.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import mimetypes
import os
import signal
import uuid
from pathlib import Path
from typing import Any

import websockets
from websockets.datastructures import Headers
from websockets.exceptions import ConnectionClosed
from websockets.http11 import Response

from sdk import Plugin

logger = logging.getLogger(__name__)

MANIFEST: dict[str, Any] = {
    "id": "gateway",
    "version": "0.1.0",
    "slots": {},
    "emits": {},
}

HELLO_TIMEOUT = 10.0


class HttpGatewayPlugin(Plugin):
    manifest = MANIFEST

    def __init__(
        self,
        *,
        host: str = "127.0.0.1",
        port: int = 18730,
        static_dir: Path | None = None,
        **client_kwargs: Any,
    ) -> None:
        super().__init__(**client_kwargs)
        self.host = host
        self.port = port
        self.static_dir = static_dir
        self._server: Any = None

    @property
    def bound_port(self) -> int:
        if self._server is None:
            return self.port
        return self._server.sockets[0].getsockname()[1]

    # -------------------------------------------------------------- lifecycle

    async def on_start(self) -> None:
        self._server = await websockets.serve(
            self._handle_browser,
            self.host,
            self.port,
            process_request=self._serve_http,
            ping_interval=30,
            ping_timeout=30,
        )
        logger.info("gateway listening on %s:%s", self.host, self.bound_port)

    async def on_stop(self) -> None:
        if self._server is not None:
            self._server.close()
            await self._server.wait_closed()

    # --------------------------------------------------------- browser facing

    async def _handle_browser(self, browser_ws: Any) -> None:
        assert self.client is not None
        kernel_ws = None
        try:
            raw = await asyncio.wait_for(browser_ws.recv(), HELLO_TIMEOUT)
            hello = json.loads(raw)
            if not isinstance(hello, dict) or hello.get("type") != "hello":
                await browser_ws.close(4002, "first frame must be hello")
                return
            conn = hello.get("conn")
            if conn is not None:
                try:
                    uuid.UUID(str(conn))
                except (ValueError, AttributeError):
                    await browser_ws.close(4002, "conn must be a UUIDv4 string")
                    return
            else:
                conn = str(uuid.uuid4())
            kernel_ws = await websockets.connect(self.client.url)
            kernel_hello = {
                "type": "hello",
                "protocol_version": hello.get("protocol_version", 1),
                "conn": conn,
                "manifest": dict(self.manifest),
                "managed": False,
                "instance_id": conn,
            }
            await kernel_ws.send(json.dumps(kernel_hello, separators=(",", ":")))

            async def browser_to_kernel() -> None:
                async for frame in browser_ws:
                    await kernel_ws.send(frame)

            async def kernel_to_browser() -> None:
                async for frame in kernel_ws:
                    await browser_ws.send(frame)

            b2k = asyncio.create_task(browser_to_kernel())
            k2b = asyncio.create_task(kernel_to_browser())
            done, pending = await asyncio.wait({b2k, k2b}, return_when=asyncio.FIRST_COMPLETED)
            for task in pending:
                task.cancel()
            for task in done:
                if not task.cancelled() and task.exception() is not None:
                    logger.debug("browser pipe ended: %s", task.exception())
            # Propagate the kernel's close code (e.g. 4009 survival) downstream.
            close_code = kernel_ws.close_code or browser_ws.close_code or 1001
            await browser_ws.close(close_code)
        except (asyncio.TimeoutError, json.JSONDecodeError):
            await browser_ws.close(4002, "first frame must be hello")
        except OSError:
            # Kernel unreachable while opening the per-browser connection.
            await browser_ws.close(1012, "kernel unavailable, retry")
        except ConnectionClosed:
            pass
        finally:
            if kernel_ws is not None:
                await kernel_ws.close()

    def _serve_http(self, connection: Any, request: Any) -> Response | None:
        path = request.path.split("?", 1)[0]
        if path == "/ws":
            return None  # proceed with the WebSocket upgrade
        if self.static_dir is None:
            return connection.respond(404, "no static directory configured\n")
        relative = "index.html" if path == "/" else path.lstrip("/")
        root = self.static_dir.resolve()
        candidate = (root / relative).resolve()
        if not str(candidate).startswith(str(root)) or not candidate.is_file():
            return connection.respond(404, "not found\n")
        body = candidate.read_bytes()
        content_type = mimetypes.guess_type(str(candidate))[0] or "application/octet-stream"
        return Response(
            200,
            "OK",
            Headers({"Content-Type": content_type, "Content-Length": str(len(body))}),
            body,
        )

    # ------------------------------------------------------------- entrypoint

    def run(self) -> None:
        parser = argparse.ArgumentParser(description="Viewer C4 http-gateway plugin")
        parser.add_argument("--kernel-ws", required=True)
        parser.add_argument("--host", default="127.0.0.1")
        parser.add_argument("--port", type=int, default=18730)
        parser.add_argument("--static", default=None, help="frontend static directory")
        args = parser.parse_args()
        logging.basicConfig(
            level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s"
        )
        self.host = args.host
        self.port = args.port
        if args.static is not None:
            self.static_dir = Path(args.static)
        managed = os.environ.get("VIEWER_MANAGED") == "1"

        async def main() -> None:
            await self.start(args.kernel_ws, managed=managed)
            stopped = asyncio.Event()
            loop = asyncio.get_running_loop()
            for signum in (signal.SIGINT, signal.SIGTERM):
                loop.add_signal_handler(signum, stopped.set)
            await stopped.wait()
            await self.stop()

        asyncio.run(main())
