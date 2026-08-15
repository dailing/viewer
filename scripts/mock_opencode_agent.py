#!/usr/bin/env python3
"""Deterministic OpenCode-flavoured ACP peer for Go plugin tests."""

from __future__ import annotations

import json
import os
import sys
import threading
import time
import uuid
from typing import Any


write_lock = threading.Lock()
cancelled: set[str] = set()


def send(value: dict[str, Any]) -> None:
    with write_lock:
        sys.stdout.write(json.dumps(value, separators=(",", ":")) + "\n")
        sys.stdout.flush()


def response(request_id: Any, result: Any = None, error: dict[str, Any] | None = None) -> None:
    frame: dict[str, Any] = {"jsonrpc": "2.0", "id": request_id}
    frame["error" if error else "result"] = error if error else result
    send(frame)


def update(session_id: str, update_value: dict[str, Any]) -> None:
    send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": session_id, "update": update_value}})


def run_prompt(request_id: Any, params: dict[str, Any]) -> None:
    session_id = str(params.get("sessionId", ""))
    blocks = params.get("prompt") or []
    text = "".join(str(block.get("text", "")) for block in blocks if isinstance(block, dict))
    update(session_id, {"sessionUpdate": "opencode_step", "phase": "working", "detail": "dialect update"})
    update(session_id, {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "opencode: "}})
    update(session_id, {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": text}})
    if "long" in text.lower():
        deadline = time.monotonic() + 20
        while session_id not in cancelled and time.monotonic() < deadline:
            time.sleep(0.02)
    if session_id in cancelled:
        cancelled.discard(session_id)
        response(request_id, {"stopReason": "cancelled"})
    else:
        response(request_id, {"stopReason": "end_turn"})


def main() -> None:
    # The production launch shape is `opencode acp`; CLI arguments are ignored.
    for raw in sys.stdin:
        try:
            request = json.loads(raw)
        except json.JSONDecodeError:
            continue
        method = request.get("method")
        params = request.get("params") or {}
        request_id = request.get("id")
        if method == "initialize":
            response(request_id, {"protocolVersion": 1, "agentCapabilities": {"loadSession": True}, "agentInfo": {"name": "viewer-mock-opencode", "version": "0.1.0"}})
        elif method == "session/new":
            response(request_id, {"sessionId": "mock-opencode-session-" + uuid.uuid4().hex})
        elif method == "session/load":
            if os.environ.get("MOCK_OPENCODE_REJECT_LOAD") == "1":
                response(request_id, error={"code": -32001, "message": "session not found"})
            else:
                response(request_id, {"loaded": True})
        elif method == "session/prompt":
            threading.Thread(target=run_prompt, args=(request_id, params), daemon=True).start()
        elif method == "session/cancel":
            cancelled.add(str(params.get("sessionId", "")))
        elif request_id is not None:
            response(request_id, error={"code": -32601, "message": "method not found"})


if __name__ == "__main__":
    main()
