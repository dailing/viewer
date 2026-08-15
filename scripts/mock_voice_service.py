#!/usr/bin/env python3
"""Deterministic WebSocket peer for the Viewer voice relay smoke suite."""

from __future__ import annotations

import argparse
import asyncio
import json
from pathlib import Path
from typing import Any

from websockets.asyncio.server import ServerConnection, serve


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--start-file", type=Path, required=True)
    return parser.parse_args()


async def handle(connection: ServerConnection, start_file: Path) -> None:
    raw_start = await connection.recv()
    if not isinstance(raw_start, str):
        raise TypeError("voice start must be a text frame")
    start: dict[str, Any] = json.loads(raw_start)
    start_file.write_text(json.dumps(start, separators=(",", ":")), encoding="utf-8")
    async for message in connection:
        if isinstance(message, bytes):
            continue
        payload = json.loads(message)
        if payload.get("type") == "stop":
            await connection.send('{"type":"processing"}')
            await connection.send('{"type":"final","text":"smoke voice ok"}')
            return


async def run(args: argparse.Namespace) -> None:
    async with serve(
        lambda connection: handle(connection, args.start_file),
        args.host,
        args.port,
        max_size=None,
    ):
        await asyncio.Future()


if __name__ == "__main__":
    asyncio.run(run(parse_args()))
