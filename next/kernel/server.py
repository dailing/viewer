"""WebSocket transport for the Viewer microkernel."""

from __future__ import annotations

import asyncio
import json
import logging
import time
from collections.abc import Mapping
from dataclasses import dataclass, field
from typing import Any

from websockets.asyncio.server import Server, ServerConnection, serve
from websockets.exceptions import ConnectionClosed

from .broker import Broker, BrokerConnection
from .protocol import (
    DEFAULT_FRAME_SIZE,
    DEFAULT_OUTBOUND_QUEUE,
    PROTOCOL_VERSION,
    HelloValidationError,
    ProtocolError,
    frame_limit_for,
    validate_hello,
    validate_post_hello_frame,
)
from .registry import ConnectionRegistry

logger = logging.getLogger(__name__)


def _close_reason(code: int, message: str) -> str:
    return json.dumps({"code": code, "message": message}, separators=(",", ":"))


@dataclass(slots=True)
class ServerConfig:
    host: str = "127.0.0.1"
    port: int = 8765
    default_frame_size: int = DEFAULT_FRAME_SIZE
    channel_size_overrides: Mapping[str, int] = field(default_factory=dict)
    outbound_queue_size: int = DEFAULT_OUTBOUND_QUEUE
    ping_interval: float = 30.0
    ping_timeout: float = 60.0
    close_timeout: float = 1.0


class KernelServer:
    def __init__(self, config: ServerConfig | None = None) -> None:
        self.config = config or ServerConfig()
        self.broker = Broker(outbound_queue_size=self.config.outbound_queue_size)
        self.registry = ConnectionRegistry(self.broker)
        self._server: Server | None = None
        self._websockets: dict[str, ServerConnection] = {}
        self._handlers: set[asyncio.Task[None]] = set()
        self._stopping = False

    @property
    def port(self) -> int:
        if self._server is None or not self._server.sockets:
            return self.config.port
        return int(self._server.sockets[0].getsockname()[1])

    async def start(self) -> None:
        if self._server is not None:
            return
        self._stopping = False
        self._server = await serve(
            self._handle,
            self.config.host,
            self.config.port,
            ping_interval=self.config.ping_interval,
            ping_timeout=self.config.ping_timeout,
            close_timeout=self.config.close_timeout,
            max_size=None,
        )
        logger.info("kernel listening on ws://%s:%s/ws", self.config.host, self.port)
        # TODO(Phase 2): the kernel's single autostart hook belongs here and may
        # launch only viewer.supervisor.

    async def stop(self) -> None:
        if self._server is None:
            return
        self._stopping = True
        reason = _close_reason(4009, "kernel is shutting down")
        # Include sockets whose hello is still in flight; successful hello has no
        # ack, so shutdown must not race registration and strand that connection.
        sockets = list(self._server.connections)
        if sockets:
            await asyncio.gather(
                *(websocket.close(code=4009, reason=reason) for websocket in sockets),
                return_exceptions=True,
            )
        self._server.close(close_connections=False)
        await self._server.wait_closed()
        if self._handlers:
            await asyncio.gather(*tuple(self._handlers), return_exceptions=True)
        self._server = None
        logger.info("kernel stopped")

    async def _handle(self, websocket: ServerConnection) -> None:
        task = asyncio.current_task()
        if task is not None:
            self._handlers.add(task)
        conn: str | None = None
        writer: asyncio.Task[None] | None = None
        registered = False
        try:
            if self._stopping:
                await websocket.close(4009, _close_reason(4009, "kernel is shutting down"))
                return
            if websocket.request.path != "/ws":
                await websocket.close(4000, _close_reason(4000, "WebSocket endpoint is /ws"))
                return
            try:
                raw = await websocket.recv()
            except ConnectionClosed:
                return
            hello = await self._parse_first_frame(websocket, raw)
            if hello is None:
                return
            conn = hello["conn"]
            try:
                state = await self.broker.add_connection(conn)
            except ValueError as exc:
                await websocket.close(4002, _close_reason(4002, str(exc)))
                return
            self._websockets[conn] = websocket
            writer = asyncio.create_task(self._writer(websocket, state))
            await self.registry.register(hello)
            registered = True
            async for raw_frame in websocket:
                await self._handle_frame(conn, hello, raw_frame)
        except ConnectionClosed:
            pass
        except Exception:
            logger.exception("unhandled connection error conn=%s", conn)
        finally:
            if conn is not None:
                # Disconnect means subscriptions disappear before lifecycle is
                # announced to remaining subscribers.
                await self.broker.remove_connection(conn)
                self._websockets.pop(conn, None)
                if registered:
                    await self.registry.deregister(conn)
            if writer is not None:
                writer.cancel()
                await asyncio.gather(writer, return_exceptions=True)
            if task is not None:
                self._handlers.discard(task)

    async def _parse_first_frame(
        self, websocket: ServerConnection, raw: str | bytes
    ) -> dict[str, Any] | None:
        if isinstance(raw, bytes):
            await websocket.close(4001, _close_reason(4001, "first frame must be hello"))
            return None
        # Bound parse work before json.loads: hello may never exceed the default.
        if len(raw.encode("utf-8")) > self.config.default_frame_size:
            await websocket.close(4002, _close_reason(4002, "hello frame exceeds size limit"))
            return None
        try:
            parsed = json.loads(raw)
        except (json.JSONDecodeError, UnicodeError):
            await websocket.close(4001, _close_reason(4001, "first frame must be hello"))
            return None
        if not isinstance(parsed, dict) or parsed.get("type") != "hello":
            await websocket.close(4001, _close_reason(4001, "first frame must be hello"))
            return None
        try:
            hello = validate_hello(parsed)
        except HelloValidationError as exc:
            await websocket.close(4002, _close_reason(4002, str(exc)))
            return None
        if hello["protocol_version"] != PROTOCOL_VERSION:
            message = (
                f"protocol version mismatch: got {hello['protocol_version']}, "
                f"want {PROTOCOL_VERSION}"
            )
            await websocket.close(4003, _close_reason(4003, message))
            return None
        return hello

    async def _handle_frame(
        self, conn: str, hello: dict[str, Any], raw: str | bytes
    ) -> None:
        if isinstance(raw, bytes):
            await self.broker.report_error(conn, "malformed_frame", "binary frames are not supported")
            return
        raw_size = len(raw.encode("utf-8"))
        # Coarse pre-parse bound: reject anything above the largest limit this
        # server would ever accept, so json.loads never sees an oversized frame.
        ceiling = max(
            [self.config.default_frame_size, *self.config.channel_size_overrides.values()]
        )
        if raw_size > ceiling:
            await self.broker.report_error(
                conn,
                "frame_too_large",
                f"frame size {raw_size} exceeds limit {ceiling}",
                {"size": raw_size, "limit": ceiling},
            )
            return
        parsed: Any = None
        try:
            parsed = json.loads(raw)
        except (json.JSONDecodeError, UnicodeError) as exc:
            await self.broker.report_error(conn, "malformed_frame", "frame is not valid JSON", {"error": str(exc)})
            return
        limit = frame_limit_for(
            parsed if isinstance(parsed, dict) else None,
            self.config.channel_size_overrides,
            self.config.default_frame_size,
        )
        if raw_size > limit:
            await self.broker.report_error(
                conn,
                "frame_too_large",
                f"frame size {raw_size} exceeds limit {limit}",
                {"size": raw_size, "limit": limit},
            )
            return
        try:
            frame = validate_post_hello_frame(parsed)
        except ProtocolError as exc:
            await self.broker.report_error(conn, exc.code, exc.message, exc.detail)
            return
        frame_type = frame["type"]
        if frame_type == "subscribe":
            await self.broker.subscribe(conn, frame["pattern"])
            return
        if frame_type == "unsubscribe":
            await self.broker.unsubscribe(conn, frame["pattern"])
            return
        now = int(time.time() * 1000)
        stamped = {
            **frame,
            "ts": now,
            "origin": {
                "plugin": hello["manifest"]["id"],
                "instance": hello.get("instance_id", "_"),
            },
            "depth": frame.get("depth", 0),
        }
        await self.broker.publish(stamped, source_conn=conn)

    async def _writer(self, websocket: ServerConnection, state: BrokerConnection) -> None:
        while True:
            priority = state.take_priority_error()
            if priority is not None:
                await websocket.send(json.dumps(priority, separators=(",", ":")))
                continue
            queue_task = asyncio.create_task(state.queue.get())
            priority_task = asyncio.create_task(state.priority_ready.wait())
            done, pending = await asyncio.wait(
                {queue_task, priority_task}, return_when=asyncio.FIRST_COMPLETED
            )
            for pending_task in pending:
                pending_task.cancel()
            await asyncio.gather(*pending, return_exceptions=True)
            if priority_task in done and state.priority_ready.is_set():
                if queue_task in done:
                    # Preserve a simultaneously dequeued ordinary frame.
                    try:
                        state.queue.put_nowait(queue_task.result())
                    except asyncio.QueueFull:
                        state.dropped += 1
                continue
            frame = queue_task.result()
            await websocket.send(json.dumps(frame, separators=(",", ":")))
