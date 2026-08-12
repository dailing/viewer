"""Async in-process publish router and retained mailbox."""

from __future__ import annotations

import asyncio
import logging
import time
from dataclasses import dataclass, field
from typing import Any

from .protocol import DEFAULT_OUTBOUND_QUEUE, MAX_DEPTH, channel_matches, validate_pattern

logger = logging.getLogger(__name__)


@dataclass(slots=True)
class BrokerConnection:
    """Broker-owned outbound state for one WebSocket connection."""

    conn: str
    queue_size: int = DEFAULT_OUTBOUND_QUEUE
    queue: asyncio.Queue[dict[str, Any]] = field(init=False)
    priority_error: dict[str, Any] | None = None
    priority_ready: asyncio.Event = field(default_factory=asyncio.Event)
    dropped: int = 0
    last_slow_notice: int = 0

    def __post_init__(self) -> None:
        self.queue = asyncio.Queue(maxsize=self.queue_size)

    def enqueue(self, frame: dict[str, Any]) -> bool:
        try:
            self.queue.put_nowait(frame)
            return True
        except asyncio.QueueFull:
            self.dropped += 1
            return False

    def set_priority_error(self, frame: dict[str, Any]) -> None:
        self.priority_error = frame
        self.priority_ready.set()

    def take_priority_error(self) -> dict[str, Any] | None:
        frame = self.priority_error
        self.priority_error = None
        self.priority_ready.clear()
        return frame


class Broker:
    """Serialize mailbox mutation, subscribe handoff, and live routing."""

    def __init__(self, *, outbound_queue_size: int = DEFAULT_OUTBOUND_QUEUE) -> None:
        self.outbound_queue_size = outbound_queue_size
        self.mailbox: dict[str, dict[str, Any]] = {}
        self.connections: dict[str, BrokerConnection] = {}
        self.subscriptions: dict[str, set[str]] = {}
        self._error_counts: dict[tuple[str, str], int] = {}
        self._lock = asyncio.Lock()

    async def add_connection(self, conn: str) -> BrokerConnection:
        async with self._lock:
            if conn in self.connections:
                raise ValueError(f"connection id already registered: {conn}")
            state = BrokerConnection(conn, self.outbound_queue_size)
            self.connections[conn] = state
            self.subscriptions[conn] = set()
            return state

    async def remove_connection(self, conn: str) -> None:
        async with self._lock:
            self.subscriptions.pop(conn, None)
            self.connections.pop(conn, None)
            # conn ids are single-use (reconnect must mint a fresh uuid4), so
            # the per-connection error mailbox is dead weight after disconnect.
            self.mailbox.pop(f"_conn:{conn}:error", None)
            for key in [key for key in self._error_counts if key[0] == conn]:
                del self._error_counts[key]

    async def subscribe(self, conn: str, pattern: str) -> None:
        validate_pattern(pattern)
        async with self._lock:
            patterns = self.subscriptions[conn]
            if pattern in patterns:
                return
            # The lock is held until every retained value is queued. A publish or
            # set can only route after this exact handoff completes.
            for channel, frame in self.mailbox.items():
                if channel_matches(pattern, channel):
                    self._enqueue_locked(conn, frame)
            patterns.add(pattern)

    async def unsubscribe(self, conn: str, pattern: str) -> None:
        validate_pattern(pattern)
        async with self._lock:
            self.subscriptions.get(conn, set()).discard(pattern)

    async def publish(self, frame: dict[str, Any], *, source_conn: str | None = None) -> bool:
        """Route a stamped publish/set frame; return false when depth rejects it."""

        if frame.get("depth", 0) >= MAX_DEPTH:
            logger.warning(
                "dropping depth-limited frame channel=%s trace_id=%s depth=%s",
                frame.get("channel"),
                frame.get("trace_id"),
                frame.get("depth"),
            )
            if source_conn is not None:
                await self.report_error(
                    source_conn,
                    "depth_exceeded",
                    f"causal publish depth must be less than {MAX_DEPTH}",
                    {"channel": frame.get("channel"), "depth": frame.get("depth")},
                )
            return False
        async with self._lock:
            if frame["type"] == "set":
                self.mailbox[frame["channel"]] = frame
            for conn, patterns in self.subscriptions.items():
                if any(channel_matches(pattern, frame["channel"]) for pattern in patterns):
                    self._enqueue_locked(conn, frame)
        return True

    async def report_error(
        self,
        conn: str,
        code: str,
        message: str,
        detail: Any | None = None,
    ) -> None:
        """Replace a connection's retained error mailbox and route its update."""

        now = int(time.time() * 1000)
        async with self._lock:
            if conn not in self.connections:
                return
            count_key = (conn, code)
            count = self._error_counts.get(count_key, 0) + 1
            self._error_counts[count_key] = count
            error_detail: dict[str, Any] = {"count": count}
            if isinstance(detail, dict):
                error_detail.update(detail)
            elif detail is not None:
                error_detail["value"] = detail
            value: dict[str, Any] = {"code": code, "message": message, "ts": now}
            if error_detail:
                value["detail"] = error_detail
            frame = {
                "type": "set",
                "channel": f"_conn:{conn}:error",
                "value": value,
                "ts": now,
                "origin": {"plugin": "kernel", "instance": "_"},
                "depth": 0,
            }
            self.mailbox[frame["channel"]] = frame
            state = self.connections[conn]
            matches = any(
                channel_matches(pattern, frame["channel"])
                for pattern in self.subscriptions.get(conn, ())
            )
            if matches:
                state.set_priority_error(frame)

    def _enqueue_locked(self, conn: str, frame: dict[str, Any]) -> None:
        state = self.connections.get(conn)
        if state is None or state.enqueue(frame):
            return
        # Notify on the first drop and powers of two. This bounds notification
        # churn while exposing a current cumulative count.
        if state.dropped == 1 or state.dropped >= max(2, state.last_slow_notice * 2):
            state.last_slow_notice = state.dropped
            asyncio.create_task(
                self.report_error(
                    conn,
                    "slow_consumer",
                    "outbound queue full; new frames were dropped",
                    {"dropped": state.dropped},
                )
            )

    async def get_retained(self, channel: str) -> dict[str, Any] | None:
        async with self._lock:
            return self.mailbox.get(channel)
