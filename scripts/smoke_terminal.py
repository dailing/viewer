#!/usr/bin/env python3
"""Black-box smoke suite for the Go terminal plugin on either kernel."""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import re
import sys
import time
from pathlib import Path
from typing import Any, Callable

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from sdk import BusClient  # noqa: E402


CALLER = {"id": "terminal-smoke", "version": "0.1.0", "slots": {}, "emits": {}}


async def wait_for(probe: Callable[[], Any], *, timeout: float = 10.0) -> Any:
    deadline = asyncio.get_running_loop().time() + timeout
    while asyncio.get_running_loop().time() < deadline:
        value = probe()
        if value:
            return value
        await asyncio.sleep(0.03)
    raise TimeoutError("condition not met within timeout")


def process_stopped(pid: int) -> bool:
    stat = Path(f"/proc/{pid}/stat")
    try:
        fields = stat.read_text().split()
    except FileNotFoundError:
        return True
    return len(fields) > 2 and fields[2] == "Z"


async def run(kernel_ws: str) -> None:
    caller = BusClient(kernel_ws, CALLER, request_timeout=15.0)
    statuses: dict[str, dict[str, Any]] = {}
    outputs: dict[str, list[dict[str, Any]]] = {}

    async def on_status(frame: dict[str, Any]) -> None:
        status = frame["value"]
        statuses[status["id"]] = status

    async def on_output(frame: dict[str, Any]) -> None:
        term_id = frame["channel"].split(":")[1]
        outputs.setdefault(term_id, []).append(frame["value"])

    await caller.subscribe("terminal:*:status", on_status)
    await caller.subscribe("terminal:*:output", on_output)
    await caller.connect()
    created: list[str] = []

    async def create() -> str:
        result = await caller.request("terminal:_:create")
        term_id = result["id"]
        created.append(term_id)
        outputs.setdefault(term_id, [])
        await wait_for(lambda: statuses.get(term_id, {}).get("state") == "running")
        return term_id

    try:
        term_id = await create()
        status = statuses[term_id]
        listed = await caller.request("terminal:_:list")
        assert term_id in [item["id"] for item in listed]
        assert status["cols"] == 120 and status["rows"] == 30
        assert status["pid"] > 0 and status["cwd"] == os.path.expanduser("~")
        assert status["shell"] in {"zsh", "bash"}
        print("PASS create/list/status (HOME cwd, defaults 120x30)")

        marker = f"echo-marker-{time.time_ns()}"
        env_marker = f"ENV_{time.time_ns()}"
        await caller.request(
            f"terminal:{term_id}:write",
            {"data": f"echo {marker}; printf '{env_marker}:%s:%s\\n' \"$TERM\" \"$COLORTERM\"\n"},
        )
        await wait_for(
            lambda: f"{env_marker}:xterm-256color:truecolor"
            in "".join(item["data"] for item in outputs[term_id])
        )
        assert all(item["seq"] > 0 and item["ts"] > 0 for item in outputs[term_id])
        print("PASS write -> output echo")

        before_frames = len(outputs[term_id])
        writes = 200
        for _ in range(writes):
            await caller.publish(f"terminal:{term_id}:write", {"data": "x"})
        await caller.publish(f"terminal:{term_id}:write", {"data": "\x15echo COALESCE_DONE\n"})
        await wait_for(
            lambda: "COALESCE_DONE" in "".join(item["data"] for item in outputs[term_id]),
            timeout=10.0,
        )
        coalesced_frames = len(outputs[term_id]) - before_frames
        assert coalesced_frames < writes // 4, (coalesced_frames, writes)
        print(f"PASS 30ms output coalescing ({writes} writes -> {coalesced_frames} frames)")

        await caller.request(f"terminal:{term_id}:resize", {"cols": 132, "rows": 43})
        await caller.request(f"terminal:{term_id}:write", {"data": "stty size\n"})
        await wait_for(lambda: "43 132" in "".join(item["data"] for item in outputs[term_id]))
        await wait_for(
            lambda: statuses.get(term_id, {}).get("cols") == 132
            and statuses.get(term_id, {}).get("rows") == 43
        )
        print("PASS resize reflected by PTY and status")

        snapshot = await caller.request(f"terminal:{term_id}:snapshot", {"limit": 200})
        entries = snapshot["entries"]
        assert marker in "".join(item["data"] for item in entries)
        assert [item["seq"] for item in entries] == sorted(item["seq"] for item in entries)
        print("PASS snapshot contains ordered history")

        await caller.request(f"terminal:{term_id}:kill")
        await wait_for(lambda: statuses.get(term_id, {}).get("state") == "killed")
        retained = await caller.request(f"terminal:{term_id}:snapshot", {"limit": 200})
        assert marker in "".join(item["data"] for item in retained["entries"])
        print("PASS killed terminal snapshot retained within 30s")

        budget_id = await create()
        await caller.request(
            f"terminal:{budget_id}:write", {"data": "yes y | head -c 3000000\n"}
        )

        async def budget_snapshot() -> dict[str, Any]:
            return await caller.request(
                f"terminal:{budget_id}:snapshot", {"limit": 500}, timeout=15.0
            )

        budget_result: dict[str, Any] = {}
        deadline = asyncio.get_running_loop().time() + 15.0
        while asyncio.get_running_loop().time() < deadline:
            budget_result = await budget_snapshot()
            if "".join(item["data"] for item in budget_result["entries"]).count("y") > 100_000:
                break
            await asyncio.sleep(0.05)
        assert budget_result["entries"]
        encoded_size = len(json.dumps(budget_result).encode())
        assert encoded_size < 900_000, encoded_size
        print(f"PASS snapshot budget ({encoded_size} bytes < 900000)")
        await caller.request(f"terminal:{budget_id}:kill")

        exit_id = await create()
        await caller.request(f"terminal:{exit_id}:write", {"data": "exit 7\n"})
        await wait_for(lambda: statuses.get(exit_id, {}).get("state") == "exited")
        assert statuses[exit_id]["exit_code"] == 7
        print("PASS natural shell exit status (code 7)")

        tree_id = await create()
        tree_marker = f"TREE_CHILD_{time.time_ns()}:"
        await caller.request(
            f"terminal:{tree_id}:write",
            {"data": f"sh -c 'sleep 300 & echo {tree_marker}$!; wait'\n"},
        )

        def child_pid() -> int | None:
            text = "".join(item["data"] for item in outputs[tree_id])
            match = re.search(re.escape(tree_marker) + r"(\d+)", text)
            return int(match.group(1)) if match else None

        child = await wait_for(child_pid)
        assert not process_stopped(child)
        await caller.request(f"terminal:{tree_id}:kill")
        await wait_for(lambda: process_stopped(child), timeout=5.0)
        print(f"PASS process-group tree termination (child pid {child} stopped)")
    finally:
        for term_id in created:
            try:
                if statuses.get(term_id, {}).get("state") == "running":
                    await caller.request(f"terminal:{term_id}:kill", timeout=5.0)
            except Exception:
                pass
        await caller.close()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--kernel-ws", required=True)
    args = parser.parse_args()
    asyncio.run(run(args.kernel_ws))
    print("PASS terminal smoke complete")


if __name__ == "__main__":
    main()
