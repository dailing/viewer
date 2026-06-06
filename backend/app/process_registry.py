from __future__ import annotations

import json
import os
import re
import time
from pathlib import Path
from typing import Any

WEAVER_RUN_DIR = Path(os.environ.get("VIEWER_WEAVER_RUN_DIR", "/tmp/viewer_run/weaver"))


def pid_alive(pid: int | None) -> bool:
    if not isinstance(pid, int) or pid <= 0:
        return False
    try:
        os.kill(pid, 0)
    except OSError:
        return False
    return True


def safe_process_name(value: str) -> str:
    cleaned = re.sub(r"[^A-Za-z0-9_.-]+", "_", value.strip())
    cleaned = cleaned.strip("._-")
    return cleaned[:180] or "process"


def registry_paths(name: str) -> tuple[Path, Path]:
    WEAVER_RUN_DIR.mkdir(parents=True, exist_ok=True)
    safe = safe_process_name(name)
    return WEAVER_RUN_DIR / f"{safe}.pid", WEAVER_RUN_DIR / f"{safe}.json"


def read_pid(name: str) -> int | None:
    pid_path, _ = registry_paths(name)
    try:
        return int(pid_path.read_text(encoding="utf-8").strip())
    except (OSError, ValueError):
        return None


def registered_process_alive(name: str) -> bool:
    return pid_alive(read_pid(name))


def process_slot_state(name: str) -> dict[str, Any]:
    pid_path, state_path = registry_paths(name)
    pid = read_pid(name)
    return {
        "name": name,
        "pid": pid,
        "alive": pid_alive(pid),
        "pid_path": pid_path.as_posix(),
        "state_path": state_path.as_posix(),
        "pid_file_exists": pid_path.exists(),
        "state_file_exists": state_path.exists(),
    }


def driver_process_name(role_name: str | None, role_id: str | None, dispatch_task_id: str | None) -> str:
    safe_role_name = safe_process_name(role_name or "role")
    safe_role_id = safe_process_name(role_id or "role_id")
    safe_task_id = safe_process_name(dispatch_task_id or "task")
    return f"driver.{safe_role_name}.{safe_role_id}.{safe_task_id}"


def write_process_state(name: str, pid: int, metadata: dict[str, Any] | None = None) -> None:
    pid_path, state_path = registry_paths(name)
    payload = {
        "name": name,
        "pid": pid,
        "alive": pid_alive(pid),
        "updated_at": time.time(),
        **(metadata or {}),
    }
    pid_path.write_text(f"{pid}\n", encoding="utf-8")
    state_path.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")


def clear_process_state(name: str, pid: int | None = None) -> None:
    pid_path, state_path = registry_paths(name)
    if pid is not None:
        current = read_pid(name)
        if current is not None and current != pid:
            return
    for path in (pid_path, state_path):
        try:
            path.unlink()
        except FileNotFoundError:
            pass
