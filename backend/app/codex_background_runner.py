import argparse
import json
import os
import subprocess
import time
from pathlib import Path


def write_state(path: Path, state: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(f"{path.suffix}.tmp")
    tmp.write_text(json.dumps(state, indent=2), encoding="utf-8")
    tmp.replace(path)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--state", required=True)
    parser.add_argument("--stdout", required=True)
    parser.add_argument("--stderr", required=True)
    parser.add_argument("--prompt", required=True)
    parser.add_argument("--cwd", required=True)
    parser.add_argument("--command", required=True)
    args = parser.parse_args()

    state_path = Path(args.state)
    stdout_path = Path(args.stdout)
    stderr_path = Path(args.stderr)
    prompt_path = Path(args.prompt)
    command = json.loads(args.command)
    started_at = time.time()
    state = {
        "runner_pid": os.getpid(),
        "codex_pid": None,
        "status": "starting",
        "exit_code": None,
        "started_at": started_at,
        "updated_at": started_at,
        "ended_at": None,
        "command": command,
        "cwd": args.cwd,
    }
    write_state(state_path, state)
    stdout_path.parent.mkdir(parents=True, exist_ok=True)
    stderr_path.parent.mkdir(parents=True, exist_ok=True)

    try:
        with prompt_path.open("rb") as prompt, stdout_path.open("ab") as stdout, stderr_path.open("ab") as stderr:
            process = subprocess.Popen(command, stdin=prompt, stdout=stdout, stderr=stderr, cwd=args.cwd, env=os.environ.copy())
            state.update({"codex_pid": process.pid, "status": "running", "updated_at": time.time()})
            write_state(state_path, state)
            exit_code = process.wait()
    except FileNotFoundError:
        exit_code = 127
    except Exception as exc:
        with stderr_path.open("a", encoding="utf-8", errors="replace") as stderr:
            stderr.write(f"viewer codex runner failed: {exc}\n")
        exit_code = 1

    ended_at = time.time()
    state.update(
        {
            "status": "exited" if exit_code == 0 else "failed",
            "exit_code": exit_code,
            "updated_at": ended_at,
            "ended_at": ended_at,
        }
    )
    write_state(state_path, state)
    return int(exit_code)


if __name__ == "__main__":
    raise SystemExit(main())
