"""C1: viewer.config-store — plugin-level configuration CRUD (framework §10.2).

Configuration here means *plugin-level* config (affects all instances, e.g.
the chat plugin's roles/agents list) — deliberately distinct from instance
state (C2). RPC surface: `config:_:get/set/list`. Every mutation republishes
the plugin's FULL config to the `config:{plugin}:config` mailbox (state =
full value, framework §5.6). Storage: one atomic-write JSON file.
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
    "id": "config-store",
    "version": "0.1.0",
    "slots": {"get": {}, "set": {}, "list": {}},
    "emits": {"config": {}},
}


def mailbox_channel(plugin_id: str) -> str:
    return f"config:{plugin_id}:config"


class ConfigStorePlugin(Plugin):
    manifest = MANIFEST

    def __init__(self, db_path: Path | None = None, **client_kwargs: Any) -> None:
        super().__init__(**client_kwargs)
        self.db_path = db_path or (Path.home() / ".view" / "config-store.json")
        self._data: dict[str, dict[str, Any]] = {}

    async def on_start(self) -> None:
        if self.db_path.is_file():
            self._data = json.loads(self.db_path.read_text())
            logger.info("loaded %d plugin configs from %s", len(self._data), self.db_path)

    def _save(self) -> None:
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        tmp = self.db_path.with_suffix(".tmp")
        tmp.write_text(json.dumps(self._data, ensure_ascii=False, indent=2))
        tmp.replace(self.db_path)

    @slot("config:_:get")
    async def get(self, ctx: Ctx) -> None:
        plugin_id, key = _plugin_and_key(ctx)
        if plugin_id is None:
            await ctx.respond_error("invalid_request", "missing required field: plugin")
            return
        config = self._data.get(plugin_id, {})
        await ctx.respond(config.get(key) if key else config)

    @slot("config:_:set")
    async def set_(self, ctx: Ctx) -> None:
        plugin_id, key = _plugin_and_key(ctx)
        if plugin_id is None or key is None:
            await ctx.respond_error("invalid_request", "missing required fields: plugin, key")
            return
        value = ctx.value.get("value")
        config = self._data.setdefault(plugin_id, {})
        if value is None:
            config.pop(key, None)
        else:
            config[key] = value
        self._save()
        # State channel: full-value replace, consumers never need an RPC (§5.6).
        assert self.client is not None
        await self.client.set(mailbox_channel(plugin_id), dict(config))
        await ctx.respond({"plugin": plugin_id, "key": key, "value": value})

    @slot("config:_:list")
    async def list_(self, ctx: Ctx) -> None:
        await ctx.respond(dict(self._data))

    def run(self) -> None:
        parser = argparse.ArgumentParser(description="Viewer C1 config-store plugin")
        parser.add_argument("--kernel-ws", required=True)
        parser.add_argument("--db", default=None, help="config JSON file path")
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


def _plugin_and_key(ctx: Ctx) -> tuple[str | None, str | None]:
    if not isinstance(ctx.value, dict):
        return None, None
    return ctx.value.get("plugin"), ctx.value.get("key")
