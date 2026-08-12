"""C3: viewer.file-service — resolve/read/hash (framework §10.2).

RPC surface: `file:_:resolve` (stat + sha256), `file:_:read` (inline content
up to a byte cap, utf-8 or base64), `file:_:hash` (sha256 only). Large-file
by-reference reads are the http-gateway data plane and out of scope here.

TODO(§16-5 unresolved): path access is currently unrestricted beyond `~`
expansion + absolutization; the allowlist/sandbox policy is an open design
question and lands here once decided.
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import hashlib
import logging
import os
import signal
from pathlib import Path
from typing import Any

from sdk import Ctx, Plugin, slot

logger = logging.getLogger(__name__)

MANIFEST: dict[str, Any] = {
    "id": "file-service",
    "version": "0.1.0",
    "slots": {"resolve": {}, "read": {}, "hash": {}},
    "emits": {},
}

DEFAULT_MAX_READ_BYTES = 1024 * 1024
_HASH_CHUNK = 64 * 1024


class FileServicePlugin(Plugin):
    manifest = MANIFEST

    # ------------------------------------------------------------------ slots

    @slot("file:_:resolve")
    async def resolve(self, ctx: Ctx) -> None:
        path = _path(ctx)
        if path is None:
            await ctx.respond_error("invalid_request", "missing required field: path")
            return
        try:
            stat = path.stat()
        except FileNotFoundError:
            await ctx.respond_error("not_found", f"no such file: {path}")
            return
        except OSError as exc:
            await ctx.respond_error("read_error", str(exc))
            return
        await ctx.respond(
            {
                "path": str(path),
                "exists": True,
                "is_dir": path.is_dir(),
                "size": stat.st_size,
                "mtime": int(stat.st_mtime),
                "sha256": await asyncio.to_thread(_sha256, path) if path.is_file() else None,
            }
        )

    @slot("file:_:read")
    async def read(self, ctx: Ctx) -> None:
        path = _path(ctx)
        if path is None:
            await ctx.respond_error("invalid_request", "missing required field: path")
            return
        max_bytes = int(ctx.value.get("max_bytes", DEFAULT_MAX_READ_BYTES))
        try:
            size = path.stat().st_size
        except FileNotFoundError:
            await ctx.respond_error("not_found", f"no such file: {path}")
            return
        except OSError as exc:
            await ctx.respond_error("read_error", str(exc))
            return
        if size > max_bytes:
            await ctx.respond_error(
                "too_large",
                f"{path} is {size} bytes, above the {max_bytes}-byte inline cap; "
                "use the gateway by-reference data plane",
            )
            return
        try:
            raw = await asyncio.to_thread(path.read_bytes)
        except OSError as exc:
            await ctx.respond_error("read_error", str(exc))
            return
        try:
            content: dict[str, Any] = {"encoding": "utf-8", "content": raw.decode("utf-8")}
        except UnicodeDecodeError:
            content = {
                "encoding": "base64",
                "content": base64.b64encode(raw).decode("ascii"),
            }
        await ctx.respond({"path": str(path), "size": size, **content})

    @slot("file:_:hash")
    async def hash_(self, ctx: Ctx) -> None:
        path = _path(ctx)
        if path is None:
            await ctx.respond_error("invalid_request", "missing required field: path")
            return
        if not path.is_file():
            await ctx.respond_error("not_found", f"no such file: {path}")
            return
        await ctx.respond({"path": str(path), "sha256": await asyncio.to_thread(_sha256, path)})

    # ------------------------------------------------------------- entrypoint

    def run(self) -> None:
        parser = argparse.ArgumentParser(description="Viewer C3 file-service plugin")
        parser.add_argument("--kernel-ws", required=True)
        args = parser.parse_args()
        logging.basicConfig(
            level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s"
        )
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


def _path(ctx: Ctx) -> Path | None:
    if not isinstance(ctx.value, dict) or not ctx.value.get("path"):
        return None
    return Path(ctx.value["path"]).expanduser().absolute()


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(_HASH_CHUNK), b""):
            digest.update(chunk)
    return digest.hexdigest()
