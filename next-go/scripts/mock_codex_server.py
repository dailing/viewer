#!/usr/bin/env python3
"""Deterministic Codex App Server JSONL subset used by Go tests and smoke fixtures."""
from __future__ import annotations

import json
import sys


def send(value: dict) -> None:
    sys.stdout.write(json.dumps(value, separators=(",", ":")) + "\n")
    sys.stdout.flush()


for line in sys.stdin:
    request = json.loads(line)
    method = request.get("method", "")
    request_id = request.get("id")
    params = request.get("params") or {}
    if request_id is None:
        continue
    if method == "initialize":
        send({"id": request_id, "result": {"userAgent": "mock-codex/1"}})
    elif method == "thread/start":
        send({"id": request_id, "result": {"thread": {"id": "mock-thread"}}})
    elif method == "thread/resume":
        send({"id": request_id, "result": {"thread": {"id": params["threadId"]}}})
    elif method == "turn/start":
        send({"id": request_id, "result": {"turn": {"id": "mock-turn", "status": "inProgress"}}})
        thread_id = params["threadId"]
        send({"method": "item/reasoning/summaryTextDelta", "params": {"threadId": thread_id, "turnId": "mock-turn", "itemId": "reason-1", "delta": "thinking"}})
        send({"method": "item/agentMessage/delta", "params": {"threadId": thread_id, "turnId": "mock-turn", "itemId": "answer-1", "delta": "mock answer"}})
        send({"method": "item/commandExecution/outputDelta", "params": {"threadId": thread_id, "turnId": "mock-turn", "itemId": "command-1", "delta": "command output"}})
        send({"method": "turn/diff/updated", "params": {"threadId": thread_id, "turnId": "mock-turn", "diff": "diff --git a/a b/a"}})
        send({"method": "thread/tokenUsage/updated", "params": {"threadId": thread_id, "tokenUsage": {"last": {"totalTokens": 10}, "modelContextWindow": 1000}}})
        send({"method": "turn/completed", "params": {"threadId": thread_id, "turn": {"id": "mock-turn", "status": "completed"}}})
    elif method == "turn/interrupt":
        send({"id": request_id, "result": {}})
    else:
        send({"id": request_id, "error": {"code": -32601, "message": method}})
