"""C2: viewer.instance-store — instance state CRUD (framework §7.2 / §10.2).

Instance state = per-instance persistent state driving behavior (a chat's
roles, cwd, session ids). Free-form JSON; the schema belongs to the owning
plugin — the store never interprets values. RPC surface:
`instance:_:get/set/delete/list`. Every mutation republishes the FULL state
to the `instance-store:{plugin}:{instance}` mailbox (§5.6); delete publishes
a null value as the tombstone (mailbox has no delete primitive).
"""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import os
import signal
from pathlib import Path
from typing import Any

from sdk import Ctx, Plugin, slot

logger = logging.getLogger(__name__)

MANIFEST: dict[str, Any] = {
    "id": "instance-store",
    "version": "0.1.0",
    "slots": {"get": {}, "set": {}, "delete": {}, "list": {}},
    "emits": {"state": {}},
}


def mailbox_channel(plugin_id: str, instance: str) -> str:
    return f"instance-store:{plugin_id}:{instance}"


class InstanceStorePlugin(Plugin):
    manifest = MANIFEST

    def __init__(self, db_path: Path | None = None, **client_kwargs: Any) -> None:
        super().__init__(**client_kwargs)
        self.db_path = db_path or (Path.home() / ".view" / "instance-store.json")
        # {plugin_id: {instance: state}}
        self._data: dict[str, dict[str, Any]] = {}

    async def on_start(self) -> None:
        if self.db_path.is_file():
            self._data = json.loads(self.db_path.read_text())
            count = sum(len(instances) for instances in self._data.values())
            logger.info("loaded %d instance states from %s", count, self.db_path)

    def _save(self) -> None:
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        tmp = self.db_path.with_suffix(".tmp")
        tmp.write_text(json.dumps(self._data, ensure_ascii=False, indent=2))
        tmp.replace(self.db_path)

    @slot("instance:_:get")
    async def get(self, ctx: Ctx) -> None:
        ref = _ref(ctx)
        if ref is None:
            await ctx.respond_error("invalid_request", "missing required fields: plugin, instance")
            return
        plugin_id, instance = ref
        state = self._data.get(plugin_id, {}).get(instance)
        key = ctx.value.get("key")
        await ctx.respond(state.get(key) if (key and isinstance(state, dict)) else state)

    @slot("instance:_:set")
    async def set_(self, ctx: Ctx) -> None:
        ref = _ref(ctx)
        if ref is None:
            await ctx.respond_error("invalid_request", "missing required fields: plugin, instance")
            return
        plugin_id, instance = ref
        key = ctx.value.get("key")
        value = ctx.value.get("value")
        instances = self._data.setdefault(plugin_id, {})
        if key is None:
            if not isinstance(value, dict):
                await ctx.respond_error(
                    "invalid_request", "whole-state replace requires a JSON object value"
                )
                return
            instances[instance] = value
        else:
            state = instances.setdefault(instance, {})
            if not isinstance(state, dict):
                state = {}
                instances[instance] = state
            if value is None:
                state.pop(key, None)
            else:
                state[key] = value
        self._save()
        assert self.client is not None
        await self.client.set(mailbox_channel(plugin_id, instance), instances[instance])
        await ctx.respond({"plugin": plugin_id, "instance": instance, "state": instances[instance]})

    @slot("instance:_:delete")
    async def delete(self, ctx: Ctx) -> None:
        ref = _ref(ctx)
        if ref is None:
            await ctx.respond_error("invalid_request", "missing required fields: plugin, instance")
            return
        plugin_id, instance = ref
        existed = self._data.get(plugin_id, {}).pop(instance, None) is not None
        self._save()
        assert self.client is not None
        await self.client.set(mailbox_channel(plugin_id, instance), None)  # tombstone
        await ctx.respond({"plugin": plugin_id, "instance": instance, "existed": existed})

    @slot("instance:_:list")
    async def list_(self, ctx: Ctx) -> None:
        plugin_id = ctx.value.get("plugin") if isinstance(ctx.value, dict) else None
        if plugin_id is None:
            await ctx.respond_error("invalid_request", "missing required field: plugin")
            return
        await ctx.respond(dict(self._data.get(plugin_id, {})))

    def run(self) -> None:
        parser = argparse.ArgumentParser(description="Viewer C2 instance-store plugin")
        parser.add_argument("--kernel-ws", required=True)
        parser.add_argument("--db", default=None, help="instance-state JSON file path")
        args = parser.parse_args()
        logging.basicConfig(
            level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s"
        )
        if args.db is not None:
            self.db_path = Path(args.db)
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


def _ref(ctx: Ctx) -> tuple[str, str] | None:
    if not isinstance(ctx.value, dict):
        return None
    plugin_id, instance = ctx.value.get("plugin"), ctx.value.get("instance")
    if not plugin_id or not instance:
        return None
    return plugin_id, instance
