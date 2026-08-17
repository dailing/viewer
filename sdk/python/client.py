"""Low-level bus client: hello, reconnect with subscription replay, RPC inbox."""

from __future__ import annotations

import asyncio
import json
import logging
import uuid
from collections.abc import Awaitable, Callable
from typing import Any

import websockets
from websockets.exceptions import ConnectionClosed

from .matching import match

logger = logging.getLogger(__name__)

PROTOCOL_VERSION = 1

FrameHandler = Callable[[dict[str, Any]], Awaitable[None]]
ErrorHandler = Callable[[dict[str, Any]], None]


class RpcError(Exception):
    """The callee answered with ``ok: false`` (protocol spec section 8)."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message


class BusClient:
    """One WebSocket connection to the kernel, with survival reconnect.

    Implements the SDK duties from the protocol spec:
    - hello handshake with a fresh client-generated ``conn`` (uuid4) per attempt;
    - auto-subscribe to the per-connection error mailbox and surface entries;
    - exponential-backoff reconnect after close 4009 / abnormal loss, with
      subscription replay (section 7);
    - RPC inbox convention: request/timeout/cancel (section 8);
    - registration barrier via the ``plugins:_:list`` mailbox (section 5.2).
    """

    def __init__(
        self,
        url: str,
        manifest: dict[str, Any],
        *,
        managed: bool = False,
        instance_id: str | None = None,
        request_timeout: float = 30.0,
        reconnect: bool = True,
        backoff_base: float = 0.5,
        backoff_cap: float = 30.0,
    ) -> None:
        self.url = url
        self.manifest = manifest
        self.managed = managed
        self.instance_id = instance_id
        self.request_timeout = request_timeout
        self.reconnect = reconnect
        self.backoff_base = backoff_base
        self.backoff_cap = backoff_cap

        self.conn: str | None = None
        self._ws: Any = None
        self._run_task: asyncio.Task[None] | None = None
        self._closing = False
        self._attempt = 0
        self._connected = asyncio.Event()
        self._handlers: dict[str, list[FrameHandler]] = {}
        self._error_handlers: list[ErrorHandler] = []
        self._pending_rpc: dict[str, asyncio.Future[Any]] = {}

    @property
    def connected(self) -> bool:
        return self._connected.is_set()

    @property
    def error_channel(self) -> str | None:
        return f"_conn:{self.conn}:error" if self.conn else None

    def on_error(self, callback: ErrorHandler) -> None:
        self._error_handlers.append(callback)

    async def connect(self) -> None:
        """Start the connection loop and wait for the first successful hello."""

        self._run_task = asyncio.create_task(self._run())
        await self._connected.wait()

    async def close(self) -> None:
        self._closing = True
        if self._ws is not None:
            await self._ws.close()
        if self._run_task is not None:
            await asyncio.gather(self._run_task, return_exceptions=True)

    # ------------------------------------------------------------------ frames

    async def publish(
        self,
        channel: str,
        value: Any,
        *,
        trace_id: str | None = None,
        depth: int = 0,
    ) -> None:
        frame: dict[str, Any] = {"type": "publish", "channel": channel, "value": value}
        if trace_id is not None:
            frame["trace_id"] = trace_id
        if depth:
            frame["depth"] = depth
        await self._send(frame)

    async def set(
        self,
        channel: str,
        value: Any,
        *,
        trace_id: str | None = None,
        depth: int = 0,
    ) -> None:
        frame: dict[str, Any] = {"type": "set", "channel": channel, "value": value}
        if trace_id is not None:
            frame["trace_id"] = trace_id
        if depth:
            frame["depth"] = depth
        await self._send(frame)

    async def subscribe(self, pattern: str, handler: FrameHandler | None = None) -> None:
        """Register a pattern (replayed on reconnect) and optionally a handler."""

        handlers = self._handlers.setdefault(pattern, [])
        if handler is not None:
            handlers.append(handler)
        if self.connected:
            await self._send({"type": "subscribe", "pattern": pattern})

    async def unsubscribe(self, pattern: str, handler: FrameHandler | None = None) -> None:
        handlers = self._handlers.get(pattern)
        if handlers is not None:
            if handler is None:
                del self._handlers[pattern]
            else:
                try:
                    handlers.remove(handler)
                except ValueError:
                    return
                if handlers:
                    return
                del self._handlers[pattern]
        if self.connected:
            await self._send({"type": "unsubscribe", "pattern": pattern})

    # --------------------------------------------------------------------- RPC

    async def request(
        self,
        channel: str,
        value: Any = None,
        *,
        timeout: float | None = None,
        trace_id: str | None = None,
        depth: int = 0,
    ) -> Any:
        """Inbox-convention RPC (spec section 8). Returns the ``result`` payload.

        Raises ``RpcError`` on an ``ok: false`` response, ``TimeoutError`` after
        ``timeout`` (default 30s), ``ConnectionError`` when the bus drops.
        """

        if self.conn is None or not self.connected:
            raise ConnectionError("not connected to the bus")
        corr = uuid.uuid4().hex
        inbox = f"_inbox:{self.conn}:{corr}"
        loop = asyncio.get_running_loop()
        future: asyncio.Future[Any] = loop.create_future()
        self._pending_rpc[corr] = future
        await self._send({"type": "subscribe", "pattern": inbox})
        payload = dict(value) if isinstance(value, dict) else ({} if value is None else {"value": value})
        payload["_reply_to"] = inbox
        payload["_corr"] = corr
        try:
            await self.publish(channel, payload, trace_id=trace_id, depth=depth)
            return await asyncio.wait_for(future, timeout or self.request_timeout)
        finally:
            self._pending_rpc.pop(corr, None)
            try:
                await self._send({"type": "unsubscribe", "pattern": inbox})
            except ConnectionError:
                pass

    async def cancel(self, channel: str, corr: str, *, depth: int = 0) -> None:
        """Best-effort cancel: the callee decides whether to honour it."""

        await self.publish(channel, {"_corr": corr, "_cancel": True}, depth=depth)

    # ------------------------------------------------------------- registration

    async def wait_registered(self, timeout: float = 5.0) -> None:
        """Barrier: observe our own entry in the ``plugins:_:list`` mailbox."""

        my_conn = self.conn
        seen = asyncio.Event()

        async def watcher(frame: dict[str, Any]) -> None:
            for entry in frame.get("value") or []:
                if isinstance(entry, dict) and entry.get("conn") == my_conn:
                    seen.set()

        pattern = "plugins:_:list"
        handlers = self._handlers.setdefault(pattern, [])
        handlers.append(watcher)
        if self.connected:
            await self._send({"type": "subscribe", "pattern": pattern})
        try:
            await asyncio.wait_for(seen.wait(), timeout)
        finally:
            handlers.remove(watcher)
            if not handlers:
                del self._handlers[pattern]
                try:
                    await self._send({"type": "unsubscribe", "pattern": pattern})
                except ConnectionError:
                    pass

    # ------------------------------------------------------------------ intern

    async def _send(self, frame: dict[str, Any]) -> None:
        ws = self._ws
        if ws is None or not self.connected:
            raise ConnectionError("not connected to the bus")
        await ws.send(json.dumps(frame, separators=(",", ":")))

    async def _run(self) -> None:
        while not self._closing:
            try:
                await self._serve_once()
            except (OSError, ConnectionClosed, asyncio.TimeoutError) as exc:
                logger.info("bus connection lost: %s", exc)
            except Exception:
                logger.exception("bus connection error")
            self._connected.clear()
            self._ws = None
            for future in self._pending_rpc.values():
                if not future.done():
                    future.set_exception(ConnectionError("bus connection lost"))
            if not self.reconnect or self._closing:
                return
            self._attempt += 1
            delay = min(self.backoff_base * (2 ** (self._attempt - 1)), self.backoff_cap)
            await asyncio.sleep(delay)

    async def _serve_once(self) -> None:
        self.conn = str(uuid.uuid4())
        hello: dict[str, Any] = {
            "type": "hello",
            "protocol_version": PROTOCOL_VERSION,
            "conn": self.conn,
            "manifest": self.manifest,
            "managed": self.managed,
        }
        if self.instance_id is not None:
            hello["instance_id"] = self.instance_id
        # max_size=None: kernel imposes no frame limit; the websockets default
        # (1 MiB) would kill the connection on oversized frames mid-RPC.
        async with websockets.connect(self.url, max_size=None) as ws:
            self._ws = ws
            await ws.send(json.dumps(hello, separators=(",", ":")))
            # SDK duty: auto-subscribe the per-connection error mailbox.
            await ws.send(json.dumps({"type": "subscribe", "pattern": self.error_channel}))
            # SDK duty: replay subscriptions after (re)hello.
            for pattern in self._handlers:
                await ws.send(json.dumps({"type": "subscribe", "pattern": pattern}))
            self._attempt = 0
            self._connected.set()
            async for raw in ws:
                await self._dispatch(json.loads(raw))

    async def _dispatch(self, frame: dict[str, Any]) -> None:
        channel = frame.get("channel", "")
        value = frame.get("value")
        if channel == self.error_channel and isinstance(value, dict):
            for callback in self._error_handlers:
                callback(value)
        if channel.startswith("_inbox:") and isinstance(value, dict):
            corr = value.get("_corr")
            future = self._pending_rpc.pop(corr, None) if corr else None
            if future is not None and not future.done():
                if value.get("ok") is False:
                    error = value.get("error") or {}
                    future.set_exception(
                        RpcError(error.get("code", "error"), error.get("message", ""))
                    )
                else:
                    future.set_result(value.get("result"))
        for pattern, handlers in list(self._handlers.items()):
            if match(pattern, channel):
                for handler in handlers:
                    await handler(frame)
