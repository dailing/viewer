#!/usr/bin/env python3
"""Seed a Viewer Agent Task DAG demo through the HTTP API."""

from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parent
GROUP_ID = "agent_task_demo"


def request(base_url: str, method: str, path: str, user: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
    separator = "&" if "?" in path else "?"
    url = f"{base_url.rstrip('/')}{path}{separator}user={urllib.parse.quote(user)}"
    data = None if payload is None else json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(url, data=data, method=method)
    if payload is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            return json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"{method} {url} failed: {exc.code} {detail}") from exc


def create_task(base_url: str, user: str, payload: dict[str, Any]) -> dict[str, Any]:
    return request(base_url, "POST", "/api/agent-tasks", user, payload)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://127.0.0.1:8000")
    parser.add_argument("--user", default="dailing")
    parser.add_argument("--workspace", default=str(ROOT))
    args = parser.parse_args()

    plan = {
        "group_id": GROUP_ID,
        "goal": "Validate the Agent Task DAG manager/executor workflow on a small deterministic demo.",
        "plan": (
            "Run one successful baseline metric task, run one variant task that should request manager review, "
            "let the manager decide whether to patch shared code or create a retry, then generate a final report."
        ),
        "context": (
            f"Demo root is {ROOT}. Shared/common code is in shared/simulate_metric.py. "
            "Executors may read and run shared code but should write custom scripts and outputs in their task-local workspace."
        ),
        "constraints": [
            "Executors may write inside their task-local workspace.",
            "Executors should not edit shared/simulate_metric.py unless a task explicitly grants that permission.",
            "Shared-code changes and DAG rescheduling belong to the manager.",
            "Long-running commands should be registered through the task process API.",
            "Final report must wait for required upstream tasks.",
        ],
        "reason": "Seed demo plan.",
    }
    request(args.base_url, "PUT", "/api/agent-tasks/plan", args.user, plan)
    request(
        args.base_url,
        "PUT",
        "/api/agent-tasks/settings",
        args.user,
        {
            "default_group_id": GROUP_ID,
            "mode": "manual",
            "default_agent": "codex",
            "default_model": None,
            "auto_tick_seconds": 10,
        },
    )

    baseline = create_task(
        args.base_url,
        args.user,
        {
            "group_id": GROUP_ID,
            "title": "Demo baseline metric run",
            "description": "Create a task-local script that runs the shared simulator in baseline mode and records the result.",
            "status": "backlog",
            "priority": 80,
            "workspace": args.workspace,
            "assigned_agent": "codex",
            "execution": {
                "mode": "agent",
                "cwd": "",
                "env": {},
                "instruction": (
                    "In your task-local workspace, write a small run_baseline.sh or Python wrapper. "
                    "Run shared/simulate_metric.py with --mode baseline --seed 1. "
                    "Save metric JSON and logs inside the task-local workspace. "
                    "If you start it in the background, register the PID. "
                    "Complete the task only after checking the output JSON."
                ),
            },
        },
    )

    variant = create_task(
        args.base_url,
        args.user,
        {
            "group_id": GROUP_ID,
            "title": "Demo variant run that should request manager",
            "description": "Attempt variant mode. It intentionally fails in shared code; executor should request manager review.",
            "status": "backlog",
            "priority": 70,
            "workspace": args.workspace,
            "assigned_agent": "codex",
            "execution": {
                "mode": "agent",
                "cwd": "",
                "env": {},
                "instruction": (
                    "In your task-local workspace, write a wrapper that runs shared/simulate_metric.py with --mode variant --seed 2. "
                    "The shared simulator intentionally raises an error for variant mode. "
                    "Do not edit shared/simulate_metric.py. Capture the failure log and call the task-scoped manager-request API "
                    "explaining that shared/common code would need a manager-owned patch or a retry plan."
                ),
            },
        },
    )

    fix = create_task(
        args.base_url,
        args.user,
        {
            "group_id": GROUP_ID,
            "title": "Demo manager-owned shared-code fix",
            "description": "Manager decides how to handle the unsupported variant mode and may patch shared code or create retry tasks.",
            "status": "backlog",
            "priority": 60,
            "workspace": args.workspace,
            "assigned_agent": "codex",
            "depends_on": [variant["id"]],
            "execution": {
                "mode": "agent",
                "cwd": "",
                "env": {},
                "instruction": (
                    "This is a manager-owned task. Inspect the variant failure. "
                    "If appropriate, patch shared/simulate_metric.py to support variant mode, or create a safer retry task. "
                    "Record the decision and artifacts."
                ),
            },
            "policy": {"requires_approval": True, "auto_dispatch": False, "max_depth": None, "max_children": None},
        },
    )

    report = create_task(
        args.base_url,
        args.user,
        {
            "group_id": GROUP_ID,
            "title": "Demo final report",
            "description": "Summarize baseline, variant/fix decision, artifacts, and DAG behavior.",
            "status": "backlog",
            "priority": 50,
            "workspace": args.workspace,
            "assigned_agent": "codex",
            "depends_on": [baseline["id"], fix["id"]],
            "execution": {
                "mode": "agent",
                "cwd": "",
                "env": {},
                "instruction": (
                    "Read dependency summaries and artifacts. Write a concise markdown report in the task-local workspace "
                    "summarizing whether manager/executor separation worked."
                ),
            },
        },
    )

    print(
        json.dumps(
            {
                "group_id": GROUP_ID,
                "tasks": {
                    "baseline": baseline["id"],
                    "variant": variant["id"],
                    "fix": fix["id"],
                    "report": report["id"],
                },
            },
            indent=2,
        )
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
