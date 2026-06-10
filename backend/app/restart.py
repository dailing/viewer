import os
import signal
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

from loguru import logger

from .process_registry import clear_process_state, process_slot_state


PROJECT_ROOT = Path(__file__).resolve().parents[2]
MANAGER_PATH = PROJECT_ROOT / "scripts" / "manage_viewer.py"
RESPONSE_FLUSH_DELAY_SECONDS = 0.35
WORKER_STOP_TIMEOUT_SECONDS = 5.0


def _admin_log_dir() -> Path:
    log_file = os.environ.get("VIEWER_LOG_FILE", "")
    if log_file:
        return Path(log_file).expanduser().resolve().parent
    return Path(os.environ.get("VIEWER_HOME", Path.home() / ".view")).expanduser() / "logs"


def _wait_for_process_exit(pid: int, timeout_seconds: float) -> bool:
    deadline = time.monotonic() + timeout_seconds
    while time.monotonic() < deadline:
        try:
            os.kill(pid, 0)
        except OSError:
            return True
        time.sleep(0.1)
    try:
        os.kill(pid, 0)
    except OSError:
        return True
    return False


def _terminate_worker() -> dict[str, Any]:
    name = "worker"
    slot = process_slot_state(name)
    pid = slot["pid"]
    result: dict[str, Any] = {
        "name": name,
        "pid": pid,
        "pid_file": slot["pid_path"],
        "status": "not_running",
    }
    if not slot["alive"] or not isinstance(pid, int):
        if slot["pid_file_exists"] or slot["state_file_exists"]:
            clear_process_state(name)
            result["status"] = "cleared_stale"
        return result

    logger.info("Stopping Super Workspace worker before server restart pid={}", pid)
    try:
        os.kill(pid, signal.SIGTERM)
    except OSError as exc:
        clear_process_state(name, pid)
        result["status"] = "missing"
        result["error"] = str(exc)
        return result

    if not _wait_for_process_exit(pid, WORKER_STOP_TIMEOUT_SECONDS):
        logger.warning("Super Workspace worker pid={} did not exit; sending SIGKILL", pid)
        try:
            os.kill(pid, signal.SIGKILL)
        except OSError:
            pass
        _wait_for_process_exit(pid, 1.0)

    clear_process_state(name, pid)
    result["status"] = "stopped"
    return result


def _run_manager(command: str, *, include_worker: bool = False) -> dict[str, Any]:
    if not MANAGER_PATH.exists():
        raise RuntimeError(f"Viewer manager does not exist: {MANAGER_PATH}")

    worker_result = _terminate_worker() if include_worker else None
    log_dir = _admin_log_dir()
    log_dir.mkdir(parents=True, exist_ok=True)
    admin_log_path = log_dir / "manager.log"
    manager_command = [
        sys.executable,
        MANAGER_PATH.as_posix(),
        command,
        "--pid",
        str(os.getpid()),
        "--delay",
        str(RESPONSE_FLUSH_DELAY_SECONDS),
    ]
    with admin_log_path.open("a", encoding="utf-8") as admin_log:
        subprocess.Popen(
            manager_command,
            cwd=PROJECT_ROOT,
            stdin=subprocess.DEVNULL,
            stdout=admin_log,
            stderr=subprocess.STDOUT,
            close_fds=True,
            start_new_session=True,
        )

    response: dict[str, Any] = {
        "status": f"{command}ing" if command == "restart" else "stopping",
        "pid": os.getpid(),
        "command": manager_command,
    }
    if worker_result is not None:
        response["worker"] = worker_result
    return response


def request_restart(*, include_worker: bool = False) -> dict[str, Any]:
    return _run_manager("restart", include_worker=include_worker)


def request_stop() -> dict[str, Any]:
    return _run_manager("stop")
