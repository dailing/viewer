"""High-level Plugin base: @slot handlers, causal ctx helpers, run() entrypoint."""

from __future__ import annotations

import argparse
import asyncio
import base64
import logging
import os
import signal
import uuid
from collections.abc import Awaitable, Callable
from pathlib import Path
from typing import Any

from .client import BusClient

logger = logging.getLogger(__name__)

# Frame budgets mirror sdk/go/busclient/assets.go: stay under the kernel's
# 1 MiB frame cap including envelope overhead.
_ONE_SHOT_BUDGET = 700 * 1024  # total base64 bytes
_CHUNK_BUDGET = 480 * 1024  # decoded bytes per file op


async def push_assets(
    client: BusClient,
    directory: str | os.PathLike[str],
    manifest: dict[str, Any] | None = None,
    *,
    timeout: float = 60.0,
) -> dict[str, Any]:
    """Push a built frontend bundle to the gateway's asset store.

    Framework section 14.3: call after the plugin is registered (``start()``
    returns); the shell loads the bundle from the ``plugins:_:assets``
    mailbox without a page refresh, and a re-push hot-reloads open panes.
    ``directory`` must contain the bundle entry ``frontend.js``. Wire mode is
    chosen automatically (one-shot vs begin/file/commit chunked sequence).
    Returns the committed asset entry ({id, url, entry, hash, ...}).
    """
    root = Path(directory)
    if not (root / "frontend.js").is_file():
        raise FileNotFoundError(f"bundle entry frontend.js missing in {root}")
    files: dict[str, bytes] = {}
    for path in sorted(root.rglob("*")):
        if not path.is_file():
            continue
        rel = path.relative_to(root)
        if any(part.startswith(".") for part in rel.parts) or rel.name == "entry.json":
            continue
        files[rel.as_posix()] = path.read_bytes()

    total_b64 = sum(len(base64.b64encode(data)) for data in files.values())
    if total_b64 <= _ONE_SHOT_BUDGET:
        return await client.request(
            "gateway:_:assets:push",
            {
                "files": [
                    {"path": path, "data_b64": base64.b64encode(data).decode()}
                    for path, data in files.items()
                ],
                "manifest": manifest,
            },
            timeout=timeout,
        )

    begin = await client.request(
        "gateway:_:assets:push", {"op": "begin", "manifest": manifest}, timeout=timeout
    )
    upload_id = begin.get("upload_id")
    if not upload_id:
        raise RuntimeError(f"assets begin returned no upload_id: {begin!r}")
    for path, data in files.items():
        for offset in range(0, len(data), _CHUNK_BUDGET):
            op: dict[str, Any] = {
                "op": "file",
                "upload_id": upload_id,
                "path": path,
                "data_b64": base64.b64encode(data[offset : offset + _CHUNK_BUDGET]).decode(),
            }
            if offset > 0:
                op["append"] = True
            await client.request("gateway:_:assets:push", op, timeout=timeout)
    return await client.request(
        "gateway:_:assets:push", {"op": "commit", "upload_id": upload_id}, timeout=timeout
    )


def slot(pattern: str) -> Callable[[Callable[..., Awaitable[None]]], Callable[..., Awaitable[None]]]:
    """Mark a Plugin method as the handler for a channel pattern."""

    def decorator(fn: Callable[..., Awaitable[None]]) -> Callable[..., Awaitable[None]]:
        fn._slot_pattern = pattern  # type: ignore[attr-defined]
        return fn

    return decorator


class Ctx:
    """Per-frame handler context: frame metadata plus causal-publish helpers.

    Causal republish rule (protocol spec section 9.1): a publish triggered by
    an incoming frame carries the same ``trace_id`` with ``depth + 1``; when
    the incoming frame started no chain, a fresh ``trace_id`` is minted.
    """

    def __init__(self, client: BusClient, frame: dict[str, Any]) -> None:
        self._client = client
        self.frame = frame

    @property
    def value(self) -> Any:
        return self.frame.get("value")

    @property
    def channel(self) -> str:
        return self.frame.get("channel", "")

    @property
    def origin(self) -> dict[str, Any] | None:
        return self.frame.get("origin")

    @property
    def ts(self) -> int | None:
        return self.frame.get("ts")

    @property
    def trace_id(self) -> str | None:
        return self.frame.get("trace_id")

    @property
    def depth(self) -> int:
        return self.frame.get("depth", 0)

    def _causal(self) -> dict[str, Any]:
        return {"trace_id": self.trace_id or uuid.uuid4().hex, "depth": self.depth + 1}

    async def emit(self, channel: str, value: Any) -> None:
        await self._client.publish(channel, value, **self._causal())

    async def set(self, channel: str, value: Any) -> None:
        await self._client.set(channel, value, **self._causal())

    async def request(self, channel: str, value: Any = None, *, timeout: float | None = None) -> Any:
        return await self._client.request(channel, value, timeout=timeout, **self._causal())

    @property
    def reply_to(self) -> str | None:
        value = self.value
        return value.get("_reply_to") if isinstance(value, dict) else None

    @property
    def corr(self) -> str | None:
        value = self.value
        return value.get("_corr") if isinstance(value, dict) else None

    @property
    def is_cancel(self) -> bool:
        value = self.value
        return bool(value.get("_cancel")) if isinstance(value, dict) else False

    async def respond(self, result: Any) -> None:
        """RPC success response (spec section 8). No-op for non-RPC frames."""

        if self.reply_to and self.corr:
            await self._client.publish(
                self.reply_to,
                {"_corr": self.corr, "ok": True, "result": result},
                **self._causal(),
            )

    async def respond_error(self, code: str, message: str) -> None:
        if self.reply_to and self.corr:
            await self._client.publish(
                self.reply_to,
                {"_corr": self.corr, "ok": False, "error": {"code": code, "message": message}},
                **self._causal(),
            )


class Plugin:
    """Base class for backend plugins.

    Subclasses set ``manifest`` (or pass one to ``__init__``) and decorate
    handler methods with ``@slot(pattern)``. Lifecycle hooks: ``on_start`` /
    ``on_stop``. ``run()`` implements the startup ABI (framework section
    14.3): ``backend/run --kernel-ws ws://...``.
    """

    manifest: dict[str, Any] = {}

    def __init__(self, manifest: dict[str, Any] | None = None, **client_kwargs: Any) -> None:
        if manifest is not None:
            self.manifest = manifest
        self.client_kwargs = client_kwargs
        self.client: BusClient | None = None

    # ---------------------------------------------------------------- hooks

    async def on_start(self) -> None:
        """Called once after hello + registration barrier."""

    async def on_stop(self) -> None:
        """Called before the connection is closed."""

    async def push_assets(
        self, directory: str | os.PathLike[str], manifest: dict[str, Any] | None = None
    ) -> dict[str, Any]:
        """Push this plugin's built frontend bundle to the gateway.

        Convenience wrapper around the module-level ``push_assets`` using
        this plugin's connected client; call from ``on_start`` or later.
        """
        if self.client is None:
            raise RuntimeError("push_assets requires a started plugin (client is None)")
        return await push_assets(self.client, directory, manifest)

    # -------------------------------------------------------------- lifecycle

    def _slots(self) -> list[tuple[str, Callable[[Ctx], Awaitable[None]]]]:
        found: list[tuple[str, Callable[[Ctx], Awaitable[None]]]] = []
        for name in dir(self):
            fn = getattr(self, name)
            pattern = getattr(fn, "_slot_pattern", None)
            if pattern is not None:
                found.append((pattern, fn))
        return found

    async def start(
        self,
        kernel_ws: str,
        *,
        managed: bool = False,
        instance_id: str | None = None,
    ) -> BusClient:
        client = BusClient(
            kernel_ws,
            self.manifest,
            managed=managed,
            instance_id=instance_id,
            **self.client_kwargs,
        )
        self.client = client
        client.on_error(self._on_protocol_error)
        for pattern, fn in self._slots():
            async def handler(frame: dict[str, Any], _fn: Callable[[Ctx], Awaitable[None]] = fn) -> None:
                await _fn(Ctx(client, frame))

            await client.subscribe(pattern, handler)
        await client.connect()
        await client.wait_registered()
        await self.on_start()
        return client

    async def stop(self) -> None:
        await self.on_stop()
        if self.client is not None:
            await self.client.close()
            self.client = None

    def _on_protocol_error(self, value: dict[str, Any]) -> None:
        logger.warning(
            "protocol error [%s] %s detail=%s",
            value.get("code"),
            value.get("message"),
            value.get("detail"),
        )

    # ------------------------------------------------------------- entrypoint

    def run(self) -> None:
        """Startup ABI entry: ``--kernel-ws`` (fixed); ``--instance-id`` is a
        plugin-internal option (framework section 14.3). ``managed`` is passed
        out-of-band by the supervisor via the ``VIEWER_MANAGED`` env var."""

        parser = argparse.ArgumentParser(description=f"Viewer plugin {self.manifest.get('id', '?')}")
        parser.add_argument("--kernel-ws", required=True, help="kernel WebSocket URL")
        parser.add_argument("--instance-id", default=None)
        args = parser.parse_args()
        logging.basicConfig(
            level=logging.INFO,
            format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        )
        managed = os.environ.get("VIEWER_MANAGED") == "1"
        asyncio.run(self._run_forever(args.kernel_ws, managed, args.instance_id))

    async def _run_forever(self, kernel_ws: str, managed: bool, instance_id: str | None) -> None:
        await self.start(kernel_ws, managed=managed, instance_id=instance_id)
        stopped = asyncio.Event()
        loop = asyncio.get_running_loop()
        for signum in (signal.SIGINT, signal.SIGTERM):
            loop.add_signal_handler(signum, stopped.set)
        await stopped.wait()
        await self.stop()
