#!/usr/bin/env python3
from __future__ import annotations

import argparse
import os
import signal
import subprocess
import time
from pathlib import Path


DEFAULT_CHILD_STOP_TIMEOUT_SECONDS = 30.0
POLL_INTERVAL_SECONDS = 0.1


class ViewerSupervisor:
    """Keep systemd's MainPID stable while replacing backend generations."""

    def __init__(self, command: list[str], cwd: Path, stop_timeout: float) -> None:
        self.command = command
        self.cwd = cwd
        self.stop_timeout = stop_timeout
        self.child: subprocess.Popen[bytes] | None = None
        self.restart_requested = False
        self.stop_requested = False

    def request_restart(self, _signum: int, _frame: object) -> None:
        self.restart_requested = True

    def request_stop(self, _signum: int, _frame: object) -> None:
        self.stop_requested = True

    def start_child(self) -> None:
        env = os.environ.copy()
        env["VIEWER_SUPERVISOR_PID"] = str(os.getpid())
        self.child = subprocess.Popen(self.command, cwd=self.cwd, env=env)
        print(
            f"Viewer supervisor pid={os.getpid()} started backend pid={self.child.pid}: {' '.join(self.command)}",
            flush=True,
        )

    def stop_child(self) -> None:
        child = self.child
        if child is None or child.poll() is not None:
            return
        child.send_signal(signal.SIGTERM)
        try:
            child.wait(timeout=self.stop_timeout)
        except subprocess.TimeoutExpired:
            print(
                f"Viewer backend pid={child.pid} did not exit after {self.stop_timeout:.0f}s; sending SIGKILL",
                flush=True,
            )
            child.kill()
            child.wait(timeout=5.0)

    def run(self) -> int:
        signal.signal(signal.SIGHUP, self.request_restart)
        signal.signal(signal.SIGTERM, self.request_stop)
        signal.signal(signal.SIGINT, self.request_stop)
        self.start_child()
        while True:
            if self.stop_requested:
                self.stop_child()
                return 0
            if self.restart_requested:
                self.restart_requested = False
                old_pid = self.child.pid if self.child is not None else None
                self.stop_child()
                if self.stop_requested:
                    return 0
                print(f"Viewer supervisor replacing backend pid={old_pid}", flush=True)
                self.start_child()
                continue
            if self.child is not None:
                return_code = self.child.poll()
                if return_code is not None:
                    print(
                        f"Viewer backend pid={self.child.pid} exited unexpectedly with status={return_code}",
                        flush=True,
                    )
                    return return_code if return_code != 0 else 1
            time.sleep(POLL_INTERVAL_SECONDS)


def parse_args() -> tuple[argparse.Namespace, list[str]]:
    parser = argparse.ArgumentParser(description="Supervise Viewer backend generations under one systemd MainPID.")
    parser.add_argument("--cwd", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--stop-timeout", type=float, default=DEFAULT_CHILD_STOP_TIMEOUT_SECONDS)
    args, command = parser.parse_known_args()
    if command[:1] == ["--"]:
        command = command[1:]
    if not command:
        parser.error("a backend command is required after --")
    return args, command


def main() -> int:
    args, command = parse_args()
    supervisor = ViewerSupervisor(command, args.cwd.resolve(), args.stop_timeout)
    return supervisor.run()


if __name__ == "__main__":
    raise SystemExit(main())
