from __future__ import annotations

import asyncio
import argparse
import os

from loguru import logger

from .process_registry import clear_process_state, process_slot_state, write_process_state
from .storage import ensure_view_home
from .super_workspace_runtime import SuperWorkspaceRuntime
from .hermes_sessions import hermes_session_manager

LEADERSHIP_CHECK_INTERVAL_SECONDS = 5.0
# Grace window before a fresh worker starts monitoring the pid file. The parent
# server (ensure_worker_process) writes our pid right after spawning us; if we
# monitored immediately we could read a pre-spawn pid file pointing at the old
# worker and declare leadership lost on our very first check.
LEADERSHIP_GRACE_SECONDS = 10.0


async def _watch_leadership(runtime: SuperWorkspaceRuntime, name: str) -> None:
    """Set runtime._leadership_lost once another worker overwrites the pid file.

    The dispatch loop then stops claiming new tasks but drains in-flight runs
    to completion before this process exits.
    """
    my_pid = os.getpid()
    await asyncio.sleep(LEADERSHIP_GRACE_SECONDS)
    while not runtime._stop.is_set() and not runtime._leadership_lost.is_set():
        slot = process_slot_state(name)
        if slot["pid_file_exists"] and slot["pid"] not in (None, my_pid):
            logger.warning(
                "Super Workspace worker leadership taken over by pid={} (mine={}); draining in-flight runs before exit",
                slot["pid"],
                my_pid,
            )
            runtime._leadership_lost.set()
            return
        await asyncio.sleep(LEADERSHIP_CHECK_INTERVAL_SECONDS)


async def main_async() -> int:
    ensure_view_home()
    notify_url = os.environ.get("VIEWER_SUPER_WORKSPACE_NOTIFY_URL") or None
    runtime = SuperWorkspaceRuntime(notify_url=notify_url)
    name = "worker"
    slot = process_slot_state(name)
    current_pid = os.getpid()
    if slot["pid_file_exists"] and slot["alive"] and slot["pid"] != current_pid:
        # Another worker is alive. Take over leadership anyway: the parent server
        # will overwrite the pid file right after spawning us, and the old worker
        # will notice via its leadership monitor, drain in-flight runs, and exit.
        logger.info(
            "Super Workspace worker taking over leadership from live pid={} (mine={}); old worker will drain and exit",
            slot["pid"],
            current_pid,
        )
    elif slot["pid_file_exists"] and not slot["alive"]:
        logger.warning("Stale Super Workspace worker pid file found; overwriting pid={} pid_file={}", slot["pid"], slot["pid_path"])
    write_process_state(name, os.getpid(), {"kind": "super_workspace_worker", "notify_url": notify_url})
    logger.info("Super Workspace worker process started pid={} notify_url={}", os.getpid(), notify_url)
    leadership_watcher = asyncio.create_task(_watch_leadership(runtime, name))
    try:
        try:
            await hermes_session_manager.start()
        except Exception:
            logger.exception("Hermes ACP startup failed; Hermes tasks will retry lazily")
        runtime._stop.clear()
        await runtime._dispatch_worker_loop()
    finally:
        leadership_watcher.cancel()
        try:
            await leadership_watcher
        except asyncio.CancelledError:
            pass
        await hermes_session_manager.shutdown()
        # clear_process_state only removes the pid file if it still points to us,
        # so a newer worker's registration is left untouched.
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
