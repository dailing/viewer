"""Connection identity registry and plugin lifecycle publication."""

from __future__ import annotations

import time
from typing import Any

from .broker import Broker


class ConnectionRegistry:
    def __init__(self, broker: Broker) -> None:
        self.broker = broker
        self._entries: dict[str, dict[str, Any]] = {}

    @property
    def entries(self) -> list[dict[str, Any]]:
        return list(self._entries.values())

    async def register(self, hello: dict[str, Any]) -> dict[str, Any]:
        now = int(time.time() * 1000)
        manifest = hello["manifest"]
        entry = {
            "id": manifest["id"],
            "instance_id": hello.get("instance_id"),
            "manifest": manifest,
            "managed": hello["managed"],
            "conn": hello["conn"],
            "connected_at": now,
        }
        self._entries[hello["conn"]] = entry
        await self._publish_list(now)
        await self._publish_lifecycle(entry, "activated", now)
        return entry

    async def deregister(self, conn: str) -> None:
        entry = self._entries.pop(conn, None)
        if entry is None:
            return
        now = int(time.time() * 1000)
        await self._publish_list(now)
        await self._publish_lifecycle(entry, "deactivated", now)

    async def _publish_list(self, now: int) -> None:
        await self.broker.publish(
            {
                "type": "set",
                "channel": "plugins:_:list",
                "value": self.entries,
                "ts": now,
                "origin": {"plugin": "kernel", "instance": "_"},
                "depth": 0,
            }
        )

    async def _publish_lifecycle(self, entry: dict[str, Any], state: str, now: int) -> None:
        await self.broker.publish(
            {
                "type": "publish",
                "channel": f"plugins:{entry['id']}:lifecycle",
                "value": {"state": state, "conn": entry["conn"]},
                "ts": now,
                "origin": {"plugin": "kernel", "instance": "_"},
                "depth": 0,
            }
        )
