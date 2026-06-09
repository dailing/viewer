import os
import subprocess
import sys
from pathlib import Path
from typing import Any


PROJECT_ROOT = Path(__file__).resolve().parents[2]
MANAGER_PATH = PROJECT_ROOT / "scripts" / "manage_viewer.py"
RESPONSE_FLUSH_DELAY_SECONDS = 0.35


def _admin_log_dir() -> Path:
    log_file = os.environ.get("VIEWER_LOG_FILE", "")
    if log_file:
        return Path(log_file).expanduser().resolve().parent
    return Path(os.environ.get("VIEWER_HOME", Path.home() / ".view")).expanduser() / "logs"


def _run_manager(command: str) -> dict[str, Any]:
    if not MANAGER_PATH.exists():
        raise RuntimeError(f"Viewer manager does not exist: {MANAGER_PATH}")

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

    return {"status": f"{command}ing" if command == "restart" else "stopping", "pid": os.getpid(), "command": manager_command}


def request_restart() -> dict[str, Any]:
    return _run_manager("restart")


def request_stop() -> dict[str, Any]:
    return _run_manager("stop")
