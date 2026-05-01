#!/usr/bin/env python3
import json
import os
import signal
import subprocess
import sys
import time
from pathlib import Path
from typing import Any


GRACEFUL_TIMEOUT_SECONDS = 30.0
FORCED_TIMEOUT_SECONDS = 5.0
RESPONSE_FLUSH_DELAY_SECONDS = 0.35


def pid_exists(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def wait_for_exit(pid: int, timeout: float) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not pid_exists(pid):
            return True
        time.sleep(0.2)
    return not pid_exists(pid)


def log(message: str) -> None:
    print(f"[{time.strftime('%Y-%m-%d %H:%M:%S')}] {message}", flush=True)


def load_state(path: Path) -> dict[str, Any]:
    return json.loads(path.read_text(encoding="utf-8"))


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: restart_viewer.py <state-json>", file=sys.stderr)
        return 2

    state_path = Path(sys.argv[1]).expanduser().resolve()
    state = load_state(state_path)
    pid = int(state["pid"])
    command = [str(item) for item in state["command"]]
    cwd = str(state["cwd"])

    time.sleep(RESPONSE_FLUSH_DELAY_SECONDS)
    log(f"Restart requested for pid={pid}: {' '.join(command)}")

    try:
        os.kill(pid, signal.SIGTERM)
        log(f"Sent SIGTERM to pid={pid}")
    except ProcessLookupError:
        log(f"Process pid={pid} already exited")

    if not wait_for_exit(pid, GRACEFUL_TIMEOUT_SECONDS):
        log(f"pid={pid} did not exit after {GRACEFUL_TIMEOUT_SECONDS:.0f}s; sending SIGKILL")
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        if not wait_for_exit(pid, FORCED_TIMEOUT_SECONDS):
            log(f"pid={pid} still appears to be alive; continuing with restart")

    helper_log_path = Path(str(state.get("helper_log") or state_path.with_suffix(".log"))).expanduser().resolve()
    helper_log_path.parent.mkdir(parents=True, exist_ok=True)
    with helper_log_path.open("a", encoding="utf-8") as output:
        subprocess.Popen(
            command,
            cwd=cwd,
            env=os.environ.copy(),
            stdin=subprocess.DEVNULL,
            stdout=output,
            stderr=subprocess.STDOUT,
            close_fds=True,
            start_new_session=True,
        )
    log("Started replacement server process")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
