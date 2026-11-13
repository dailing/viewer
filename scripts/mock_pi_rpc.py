#!/usr/bin/env python3
"""Deterministic Pi native-RPC peer for Go client and plugin tests."""

from __future__ import annotations

import json
import os
import sys
import threading
import time
from typing import Any

write_lock = threading.Lock()
abort_requested = threading.Event()
dialog_cancelled = threading.Event()
current_session = "/tmp/mock-pi-session-a.jsonl"
current_model = {"provider": "mockprov", "id": "model-a", "contextWindow": 128000}
MODELS = [
    current_model,
    {"provider": "mockprov", "id": "model-b", "contextWindow": 256000},
    {"provider": "otherprov", "id": "other-model", "contextWindow": 64000},
]


def send(value: dict[str, Any]) -> None:
    with write_lock:
        sys.stdout.write(json.dumps(value, separators=(",", ":")) + "\n")
        sys.stdout.flush()


def respond(request: dict[str, Any], data: Any = None, error: str | None = None) -> None:
    frame: dict[str, Any] = {"id": request.get("id"), "type": "response", "command": request.get("type"), "success": error is None}
    if error is not None:
        frame["error"] = error
    elif data is not None:
        frame["data"] = data
    send(frame)


def assistant(stop_reason: str = "stop", error: str | None = None) -> dict[str, Any]:
    message: dict[str, Any] = {
        "role": "assistant",
        "content": [],
        "provider": current_model["provider"],
        "model": current_model["id"],
        "stopReason": stop_reason,
        "usage": {"input": 100, "output": 10, "totalTokens": 110},
    }
    if error:
        message["errorMessage"] = error
    return message


def message_update(update: dict[str, Any]) -> None:
    send({"type": "message_update", "usage": {"input": 100, "output": 1, "totalTokens": 101}, "assistantMessageEvent": update})


def tool_lifecycle(call_id: str, name: str, args: dict[str, Any], output: str) -> None:
    message_update({"type": "toolcall_end", "contentIndex": 0, "toolCall": {"type": "toolCall", "id": call_id, "name": name, "arguments": args}})
    send({"type": "tool_execution_start", "toolCallId": call_id, "toolName": name, "args": args})
    send({"type": "tool_execution_update", "toolCallId": call_id, "toolName": name, "args": args, "partialResult": {"content": [{"type": "text", "text": "partial"}]}})
    result: dict[str, Any] = {"content": [{"type": "text", "text": output}], "details": {}}
    if name == "edit":
        result["details"] = {"diff": "diff --git a/a.txt b/a.txt"}
    send({"type": "tool_execution_end", "toolCallId": call_id, "toolName": name, "result": result, "isError": False, "durationMs": 12})


def run_prompt(request: dict[str, Any]) -> None:
    text = str(request.get("message", ""))
    command = text.strip().lower()
    send({"type": "agent_start"})
    if command == "long":
        deadline = time.monotonic() + 20
        while not abort_requested.is_set() and time.monotonic() < deadline:
            time.sleep(0.01)
        abort_requested.clear()
        send({"type": "message_end", "message": assistant("aborted")})
        send({"type": "agent_end", "messages": [], "willRetry": False})
        send({"type": "agent_settled", "aborted": True})
        return
    if command == "deferred":
        send({"type": "message_end", "message": assistant("deferred")})
        send({"type": "agent_settled", "aborted": False})
        return
    if command == "error":
        send({"type": "auto_retry_end", "success": False, "attempt": 3, "finalError": "provider unavailable"})
        send({"type": "message_end", "message": assistant("error", "provider unavailable")})
        send({"type": "agent_settled", "aborted": False})
        return

    # Dialogs must be auto-cancelled by the client; otherwise this mock waits
    # here and the protocol test times out.
    send({"type": "extension_ui_request", "id": "dialog-1", "method": "select", "title": "Choose", "options": ["A", "B"]})
    deadline = time.monotonic() + 2
    while not dialog_cancelled.is_set() and time.monotonic() < deadline:
        time.sleep(0.005)
    dialog_cancelled.clear()
    send({"type": "turn_start"})
    send({"type": "message_start", "message": {"role": "assistant", "content": [], "stopReason": "pending"}})
    message_update({"type": "thinking_delta", "contentIndex": 0, "delta": "considering"})
    message_update({"type": "text_delta", "contentIndex": 1, "delta": "pi: " + text})
    message_update({"type": "text_end", "contentIndex": 1, "content": "pi: " + text})
    tool_lifecycle("call-read", "read", {"path": "/tmp/a.txt"}, "file contents")
    tool_lifecycle("call-edit", "edit", {"path": "/tmp/a.txt"}, "edited")
    tool_lifecycle("call-bash", "bash", {"command": "printf done"}, "done")
    send({"type": "message_end", "message": assistant()})
    send({"type": "turn_end", "message": assistant(), "toolResults": []})
    send({"type": "agent_end", "messages": [], "willRetry": False})
    send({"type": "agent_settled", "aborted": False})


def main() -> None:
    global current_session, current_model
    for raw in sys.stdin:
        try:
            request = json.loads(raw)
        except json.JSONDecodeError:
            continue
        record_type = request.get("type")
        if record_type == "extension_ui_response":
            if request.get("id") == "dialog-1" and request.get("cancelled") is True:
                dialog_cancelled.set()
            continue
        if record_type == "get_state":
            respond(request, {"sessionFile": current_session, "sessionId": "mock-session-id", "thinkingLevel": "medium", "model": current_model, "isStreaming": False})
        elif record_type == "switch_session":
            path = str(request.get("sessionPath", ""))
            if os.environ.get("MOCK_PI_REJECT_SWITCH") == "1" or not path:
                respond(request, error="session not found")
            else:
                current_session = path
                respond(request, {"cancelled": False})
        elif record_type == "set_model":
            provider, model_id = request.get("provider"), request.get("modelId")
            match = next((model for model in MODELS if model["provider"] == provider and model["id"] == model_id), None)
            if match is None:
                respond(request, error=f"Model not found: {provider}/{model_id}")
            else:
                current_model = match
                respond(request, match)
        elif record_type == "set_thinking_level":
            if request.get("level") not in {"off", "minimal", "low", "medium", "high", "xhigh", "max"}:
                respond(request, error="invalid thinking level")
            else:
                respond(request)
        elif record_type == "get_available_models":
            respond(request, {"models": MODELS})
        elif record_type == "prompt":
            text = str(request.get("message", ""))
            if text == "handled":
                respond(request, {"disposition": "handled"})
            else:
                respond(request, {"disposition": "started"})
                threading.Thread(target=run_prompt, args=(request,), daemon=True).start()
        elif record_type == "abort":
            abort_requested.set()
            respond(request)
        elif record_type == "get_session_stats":
            respond(request, {"sessionFile": current_session, "sessionId": "mock-session-id", "contextUsage": {"tokens": 3210, "contextWindow": current_model["contextWindow"], "percent": 2.5}})
        else:
            respond(request, error=f"unknown command: {record_type}")


if __name__ == "__main__":
    main()
