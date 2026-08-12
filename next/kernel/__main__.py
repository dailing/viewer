"""Command-line entry point for the Viewer kernel."""

from __future__ import annotations

import argparse
import asyncio
import logging
import signal

from .server import KernelServer, ServerConfig


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Viewer microkernel message bus")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8765)
    return parser.parse_args()


async def run(host: str, port: int) -> None:
    server = KernelServer(ServerConfig(host=host, port=port))
    stopped = asyncio.Event()
    loop = asyncio.get_running_loop()
    for signum in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(signum, stopped.set)
    await server.start()
    try:
        await stopped.wait()
    finally:
        await server.stop()


def main() -> None:
    args = parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    asyncio.run(run(args.host, args.port))


if __name__ == "__main__":
    main()
