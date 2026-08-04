#!/usr/bin/env python3
"""Run the inference-routing worktree with isolated state and process registries."""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path


PROJECT_ROOT = Path(__file__).resolve().parents[1]
INSTANCE_ROOT = PROJECT_ROOT / ".test-instance"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=19089)
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--serve-dir", type=Path, default=Path.home() / "Sync")
    parser.add_argument("--build-frontend", action="store_true")
    parser.add_argument("--debug", action="store_true")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    config_dir = INSTANCE_ROOT / "config"
    data_dir = INSTANCE_ROOT / "data"
    run_dir = INSTANCE_ROOT / "run"
    env = {
        **os.environ,
        "VIEWER_CONFIG_DIR": config_dir.as_posix(),
        "VIEWER_DATA_DIR": data_dir.as_posix(),
        "VIEWER_HERMES_RUN_DIR": (run_dir / "hermes").as_posix(),
        "VIEWER_WEAVER_RUN_DIR": (run_dir / "weaver").as_posix(),
    }
    command = [
        sys.executable,
        (PROJECT_ROOT / "run.py").as_posix(),
        "--serve-dir",
        args.serve_dir.expanduser().resolve().as_posix(),
        "--host",
        args.host,
        "--port",
        str(args.port),
        "--config-dir",
        config_dir.as_posix(),
        "--data-dir",
        data_dir.as_posix(),
        "--no-voice",
    ]
    if args.build_frontend:
        command.append("--build-frontend")
    if args.debug:
        command.append("--debug")
    raise SystemExit(subprocess.call(command, cwd=PROJECT_ROOT, env=env))


if __name__ == "__main__":
    main()
