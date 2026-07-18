from __future__ import annotations

import asyncio
import argparse
import os

from loguru import logger

from .process_registry import clear_process_state, process_slot_state, write_process_state
from .storage import ensure_view_home
from .super_workspace_runtime import SuperWorkspaceRuntime
from .hermes_sessions import hermes_session_manager


async def main_async() -> int:
    ensure_view_home()
    notify_url = os.environ.get("VIEWER_SUPER_WORKSPACE_NOTIFY_URL") or None
    runtime = SuperWorkspaceRuntime(notify_url=notify_url)
    name = "worker"
    slot = process_slot_state(name)
    current_pid = os.getpid()
    if slot["pid_file_exists"] and slot["alive"] and slot["pid"] != current_pid:
        logger.error(
            "Super Workspace worker pid file already points to a live process; exiting pid={} pid_file={}",
            slot["pid"],
            slot["pid_path"],
        )
        return 1
    if slot["pid_file_exists"] and not slot["alive"]:
        logger.warning("Stale Super Workspace worker pid file found; overwriting pid={} pid_file={}", slot["pid"], slot["pid_path"])
    write_process_state(name, os.getpid(), {"kind": "super_workspace_worker", "notify_url": notify_url})
    logger.info("Super Workspace worker process started pid={} notify_url={}", os.getpid(), notify_url)
    try:
        try:
            await hermes_session_manager.start()
        except Exception:
            logger.exception("Hermes ACP startup failed; Hermes tasks will retry lazily")
        runtime._stop.clear()
        await runtime._dispatch_worker_loop()
    finally:
        await hermes_session_manager.shutdown()
        clear_process_state(name, os.getpid())
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Run the Super Workspace DB dispatch worker.")
    parser.parse_args()
    try:
        return asyncio.run(main_async())
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    raise SystemExit(main())
