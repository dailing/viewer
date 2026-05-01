#!/usr/bin/env python3
import argparse
import json
import os
import signal
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timezone
from hashlib import sha1
from pathlib import Path
from typing import Any


PROJECT_ROOT = Path(__file__).resolve().parents[1]
RUN_SCRIPT = PROJECT_ROOT / "run.py"
PROJECT_KEY = sha1(PROJECT_ROOT.as_posix().encode("utf-8")).hexdigest()[:12]
STATE_DIR = Path(tempfile.gettempdir()) / "viewer-process-manager" / PROJECT_KEY
PID_FILE = STATE_DIR / "viewer.pid"
STATE_FILE = STATE_DIR / "viewer.json"
LOG_FILE = STATE_DIR / "viewer.log"
DEFAULT_COMMAND = ["uv", "run", RUN_SCRIPT.name, "--build-frontend", "--debug", "-p", "18888"]
GRACEFUL_TIMEOUT_SECONDS = 30.0
FORCED_TIMEOUT_SECONDS = 5.0


def now() -> str:
    return datetime.now(timezone.utc).isoformat()


def ensure_state_dir() -> None:
    STATE_DIR.mkdir(parents=True, exist_ok=True)


def log(message: str) -> None:
    ensure_state_dir()
    line = f"[{time.strftime('%Y-%m-%d %H:%M:%S')}] {message}"
    print(line, flush=True)
    with LOG_FILE.open("a", encoding="utf-8") as handle:
        handle.write(f"{line}\n")


def pid_exists(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def read_pid() -> int | None:
    try:
        value = PID_FILE.read_text(encoding="utf-8").strip()
        return int(value) if value else None
    except (OSError, ValueError):
        return None


def active_pid(explicit_pid: int | None = None) -> int | None:
    pid = explicit_pid if explicit_pid is not None else read_pid()
    if pid and pid_exists(pid):
        return pid
    return None


def wait_for_exit(pid: int, timeout: float) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not pid_exists(pid):
            return True
        time.sleep(0.2)
    return not pid_exists(pid)


def write_state(pid: int, command: list[str]) -> None:
    ensure_state_dir()
    PID_FILE.write_text(f"{pid}\n", encoding="utf-8")
    STATE_FILE.write_text(
        json.dumps(
            {
                "pid": pid,
                "command": command,
                "cwd": PROJECT_ROOT.as_posix(),
                "started_at": now(),
                "log_file": LOG_FILE.as_posix(),
                "pid_file": PID_FILE.as_posix(),
            },
            indent=2,
        ),
        encoding="utf-8",
    )


def clear_state(pid: int | None = None) -> None:
    current_pid = read_pid()
    if pid is None or current_pid == pid:
        for path in (PID_FILE, STATE_FILE):
            try:
                path.unlink()
            except FileNotFoundError:
                pass


def command_from_args(extra: list[str]) -> list[str]:
    return extra if extra else DEFAULT_COMMAND


def start(extra: list[str]) -> dict[str, Any]:
    running = active_pid()
    if running:
        return {"status": "running", "pid": running, "pid_file": PID_FILE.as_posix(), "log_file": LOG_FILE.as_posix()}

    command = command_from_args(extra)
    ensure_state_dir()
    with LOG_FILE.open("a", encoding="utf-8") as output:
        process = subprocess.Popen(
            command,
            cwd=PROJECT_ROOT,
            env=os.environ.copy(),
            stdin=subprocess.DEVNULL,
            stdout=output,
            stderr=subprocess.STDOUT,
            close_fds=True,
            start_new_session=True,
        )
    write_state(process.pid, command)
    log(f"Started viewer pid={process.pid}: {' '.join(command)}")
    return {"status": "started", "pid": process.pid, "pid_file": PID_FILE.as_posix(), "log_file": LOG_FILE.as_posix(), "command": command}


def stop(explicit_pid: int | None = None) -> dict[str, Any]:
    pid = active_pid(explicit_pid)
    if not pid:
        clear_state(explicit_pid)
        return {"status": "stopped", "pid": explicit_pid, "pid_file": PID_FILE.as_posix()}

    log(f"Stopping viewer pid={pid}")
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        clear_state(pid)
        return {"status": "stopped", "pid": pid, "pid_file": PID_FILE.as_posix()}

    if not wait_for_exit(pid, GRACEFUL_TIMEOUT_SECONDS):
        log(f"pid={pid} did not exit after {GRACEFUL_TIMEOUT_SECONDS:.0f}s; sending SIGKILL")
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        wait_for_exit(pid, FORCED_TIMEOUT_SECONDS)

    clear_state(pid)
    return {"status": "stopped", "pid": pid, "pid_file": PID_FILE.as_posix()}


def restart(extra: list[str], explicit_pid: int | None = None) -> dict[str, Any]:
    stopped = stop(explicit_pid)
    started = start(extra)
    return {"status": "restarted", "stopped": stopped, "started": started}


def status() -> dict[str, Any]:
    pid = read_pid()
    return {
        "status": "running" if pid and pid_exists(pid) else "stopped",
        "pid": pid,
        "pid_file": PID_FILE.as_posix(),
        "state_file": STATE_FILE.as_posix(),
        "log_file": LOG_FILE.as_posix(),
    }


def print_result(result: dict[str, Any]) -> None:
    print(json.dumps(result, indent=2), flush=True)


def parse_args() -> tuple[argparse.Namespace, list[str]]:
    parser = argparse.ArgumentParser(description="Manage the local live file viewer backend.")
    parser.add_argument("command", choices=["start", "stop", "restart", "status"])
    parser.add_argument("--pid", type=int, default=None, help="Explicit process id to stop before stop/restart.")
    parser.add_argument("--delay", type=float, default=0.0, help="Seconds to wait before running the command.")
    args, extra = parser.parse_known_args()
    return args, extra


def main() -> int:
    args, extra = parse_args()
    extra = extra[1:] if extra[:1] == ["--"] else extra
    if args.delay > 0:
        time.sleep(args.delay)

    if args.command == "start":
        print_result(start(extra))
    elif args.command == "stop":
        print_result(stop(args.pid))
    elif args.command == "restart":
        print_result(restart(extra, args.pid))
    else:
        print_result(status())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
