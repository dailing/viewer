"""C0: viewer.supervisor — spawns and supervises all backend plugin processes.

Framework section 9: the kernel's only autostart launches this plugin; it then
launches C1-C4 and every functional plugin as `backend/run --kernel-ws <url>`
subprocesses (one per plugin), restarts them with exponential backoff, applies
a crash-loop circuit breaker, captures per-plugin logs, and owns the plugin
management RPC surface. It uses only stock bus primitives — it is the first
user of "plugins may spawn subprocesses".
"""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import os
import time
from collections import deque
from pathlib import Path
from typing import Any

from sdk import Ctx, Plugin, slot

logger = logging.getLogger(__name__)

MANIFEST: dict[str, Any] = {
    "id": "supervisor",
    "version": "0.1.0",
    "slots": {"restart": {}, "states": {}},
    "emits": {"states": {}, "lifecycle": {}},
}

STATE_STARTING = "starting"
STATE_RUNNING = "running"
STATE_CRASHED = "crashed"
STATE_BROKEN = "broken"
STATE_STOPPED = "stopped"


class ManagedPlugin:
    """One supervised plugin process and its restart policy state."""

    def __init__(self, plugin_id: str, path: Path) -> None:
        self.id = plugin_id
        self.path = path
        self.process: asyncio.subprocess.Process | None = None
        self.state = STATE_STOPPED
        self.exit_code: int | None = None
        self.crash_times: deque[float] = deque()
        self.restart_task: asyncio.Task[None] | None = None
        self.watch_task: asyncio.Task[None] | None = None
        self.log_handle: Any = None

    @property
    def pid(self) -> int | None:
        return self.process.pid if self.process else None


class SupervisorPlugin(Plugin):
    manifest = MANIFEST

    def __init__(
        self,
        registry_path: Path | None = None,
        *,
        log_dir: Path | None = None,
        backoff_base: float = 1.0,
        backoff_cap: float = 30.0,
        breaker_max_crashes: int = 5,
        breaker_window: float = 60.0,
        **client_kwargs: Any,
    ) -> None:
        super().__init__(**client_kwargs)
        self.registry_path = registry_path
        self.log_dir = log_dir or (Path.home() / ".view" / "logs")
        self.backoff_base = backoff_base
        self.backoff_cap = backoff_cap
        self.breaker_max_crashes = breaker_max_crashes
        self.breaker_window = breaker_window
        self.kernel_ws = ""
        self._managed: dict[str, ManagedPlugin] = {}
        self._stopping = False

    # -------------------------------------------------------------- lifecycle

    async def on_start(self) -> None:
        assert self.client is not None
        assert self.registry_path is not None
        self.kernel_ws = self.client.url
        self.log_dir.mkdir(parents=True, exist_ok=True)
        for plugin_id, path in self._load_registry().items():
            managed = ManagedPlugin(plugin_id, path)
            self._managed[plugin_id] = managed
            await self._spawn(managed)
        await self._publish_states()

    async def on_stop(self) -> None:
        self._stopping = True
        for managed in self._managed.values():
            if managed.restart_task is not None:
                managed.restart_task.cancel()
            if managed.watch_task is not None:
                managed.watch_task.cancel()
        await asyncio.gather(
            *(self._terminate(managed) for managed in self._managed.values()),
            return_exceptions=True,
        )
        for managed in self._managed.values():
            if managed.log_handle is not None:
                managed.log_handle.close()

    def _load_registry(self) -> dict[str, Path]:
        """Registry JSON: {"plugins": [{"id": ..., "path": ..., "enabled": ...}]}."""

        assert self.registry_path is not None
        data = json.loads(self.registry_path.read_text())
        result: dict[str, Path] = {}
        for entry in data.get("plugins", []):
            if not entry.get("enabled", True):
                continue
            run = Path(entry["path"]) / "backend" / "run"
            if not run.is_file():
                logger.error("plugin %s has no executable backend/run at %s", entry["id"], run)
                continue
            result[entry["id"]] = Path(entry["path"])
        return result

    # ------------------------------------------------------------- processes

    async def _spawn(self, managed: ManagedPlugin) -> None:
        run = managed.path / "backend" / "run"
        managed.log_handle = open(self.log_dir / f"{managed.id}.log", "ab")  # noqa: SIM115
        env = {**os.environ, "VIEWER_MANAGED": "1"}
        managed.process = await asyncio.create_subprocess_exec(
            str(run),
            "--kernel-ws",
            self.kernel_ws,
            env=env,
            stdout=managed.log_handle,
            stderr=asyncio.subprocess.STDOUT,
        )
        managed.state = STATE_STARTING
        managed.exit_code = None
        managed.watch_task = asyncio.create_task(self._watch(managed))
        logger.info("spawned plugin %s pid=%s", managed.id, managed.process.pid)
        await self._publish_states()

    async def _terminate(self, managed: ManagedPlugin) -> None:
        process = managed.process
        if process is None or process.returncode is not None:
            return
        process.terminate()
        try:
            await asyncio.wait_for(process.wait(), 2.0)
        except asyncio.TimeoutError:
            process.kill()
            await process.wait()
        managed.state = STATE_STOPPED

    async def _watch(self, managed: ManagedPlugin) -> None:
        assert managed.process is not None
        exit_code = await managed.process.wait()
        if self._stopping:
            return
        managed.exit_code = exit_code
        now = time.monotonic()
        managed.crash_times.append(now)
        while managed.crash_times and now - managed.crash_times[0] > self.breaker_window:
            managed.crash_times.popleft()
        logger.warning("plugin %s exited code=%s", managed.id, exit_code)
        await self._publish_lifecycle(managed.id, "crashed", {"exit_code": exit_code})
        if len(managed.crash_times) >= self.breaker_max_crashes:
            managed.state = STATE_BROKEN
            logger.error(
                "plugin %s crash-looped (%s crashes in %.0fs); circuit breaker open",
                managed.id,
                len(managed.crash_times),
                self.breaker_window,
            )
            await self._publish_states()
            return
        managed.state = STATE_CRASHED
        await self._publish_states()
        attempt = len(managed.crash_times)
        delay = min(self.backoff_base * (2 ** (attempt - 1)), self.backoff_cap)
        managed.restart_task = asyncio.create_task(self._restart_after(managed, delay))

    async def _restart_after(self, managed: ManagedPlugin, delay: float) -> None:
        try:
            await asyncio.sleep(delay)
        except asyncio.CancelledError:
            return
        if self._stopping or managed.state == STATE_BROKEN:
            return
        await self._spawn(managed)
        await self._publish_lifecycle(managed.id, "restarted", {"pid": managed.pid})

    # ------------------------------------------------------------------ slots

    @slot("plugins:_:list")
    async def track_registry(self, ctx: Ctx) -> None:
        """A managed plugin appearing in the kernel registry => running."""

        online = {
            entry.get("manifest", {}).get("id")
            for entry in (ctx.value or [])
            if isinstance(entry, dict)
        }
        changed = False
        for managed in self._managed.values():
            if managed.state == STATE_STARTING and managed.id in online:
                managed.state = STATE_RUNNING
                managed.crash_times.clear()  # lived long enough to hello: reset breaker
                changed = True
        if changed:
            await self._publish_states()

    @slot("supervisor:_:restart")
    async def restart_rpc(self, ctx: Ctx) -> None:
        plugin_id = ctx.value.get("id") if isinstance(ctx.value, dict) else None
        managed = self._managed.get(plugin_id) if plugin_id else None
        if managed is None:
            await ctx.respond_error("not_found", f"no such managed plugin: {plugin_id}")
            return
        if managed.restart_task is not None:
            managed.restart_task.cancel()
        if managed.watch_task is not None:
            managed.watch_task.cancel()
        await self._terminate(managed)
        managed.crash_times.clear()
        await self._spawn(managed)
        await self._publish_lifecycle(managed.id, "restarted", {"pid": managed.pid})
        await ctx.respond({"id": managed.id, "pid": managed.pid})

    # ------------------------------------------------------------------ emits

    async def _publish_states(self) -> None:
        if self.client is None or not self.client.connected:
            return
        value = {
            plugin_id: {
                "state": managed.state,
                "pid": managed.pid,
                "exit_code": managed.exit_code,
                "crashes": len(managed.crash_times),
            }
            for plugin_id, managed in self._managed.items()
        }
        await self.client.set("supervisor:_:states", value)

    async def _publish_lifecycle(self, plugin_id: str, state: str, extra: dict[str, Any]) -> None:
        if self.client is None or not self.client.connected:
            return
        await self.client.publish(
            f"plugins:{plugin_id}:lifecycle",
            {"state": state, **extra},
        )

    # ------------------------------------------------------------- entrypoint

    def run(self) -> None:
        parser = argparse.ArgumentParser(description="Viewer C0 supervisor plugin")
        parser.add_argument("--kernel-ws", required=True)
        parser.add_argument("--registry", required=True, help="plugin registry JSON path")
        parser.add_argument("--log-dir", default=None)
        args = parser.parse_args()
        logging.basicConfig(
            level=logging.INFO,
            format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        )
        self.registry_path = Path(args.registry)
        if args.log_dir is not None:
            self.log_dir = Path(args.log_dir)
        import os as _os
        import signal as _signal

        managed = _os.environ.get("VIEWER_MANAGED") == "1"

        async def main() -> None:
            await self.start(args.kernel_ws, managed=managed)
            stopped = asyncio.Event()
            loop = asyncio.get_running_loop()
            for signum in (_signal.SIGINT, _signal.SIGTERM):
                loop.add_signal_handler(signum, stopped.set)
            await stopped.wait()
            await self.stop()

        asyncio.run(main())
