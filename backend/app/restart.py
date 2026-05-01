import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any


PROJECT_ROOT = Path(__file__).resolve().parents[2]
HELPER_PATH = PROJECT_ROOT / "scripts" / "restart_viewer.py"


def _restart_dir() -> Path:
    log_file = os.environ.get("VIEWER_LOG_FILE", "")
    if log_file:
        return Path(log_file).expanduser().resolve().parent
    return PROJECT_ROOT / "logs"


def current_restart_command() -> list[str]:
    return [sys.executable, *sys.argv]


def request_restart() -> dict[str, Any]:
    if not HELPER_PATH.exists():
        raise RuntimeError(f"Restart helper does not exist: {HELPER_PATH}")

    restart_dir = _restart_dir()
    restart_dir.mkdir(parents=True, exist_ok=True)
    state_path = restart_dir / f"restart-{os.getpid()}.json"
    helper_log_path = restart_dir / "restart.log"
    state = {
        "pid": os.getpid(),
        "command": current_restart_command(),
        "cwd": os.getcwd(),
        "helper_log": helper_log_path.as_posix(),
    }
    state_path.write_text(json.dumps(state, indent=2), encoding="utf-8")

    with helper_log_path.open("a", encoding="utf-8") as helper_log:
        subprocess.Popen(
            [sys.executable, HELPER_PATH.as_posix(), state_path.as_posix()],
            cwd=PROJECT_ROOT,
            stdin=subprocess.DEVNULL,
            stdout=helper_log,
            stderr=subprocess.STDOUT,
            close_fds=True,
            start_new_session=True,
        )

    return {"status": "restarting", "pid": state["pid"], "command": state["command"]}
