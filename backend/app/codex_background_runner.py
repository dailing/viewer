import argparse
import hashlib
import json
import os
import sqlite3
import subprocess
import threading
import time
from pathlib import Path
from typing import Any

try:
    from .process_registry import driver_process_name, process_slot_state, write_process_state
except ImportError:
    import sys

    sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
    from backend.app.process_registry import driver_process_name, process_slot_state, write_process_state


CODEX_ROLLOUT_ROOT = Path.home() / ".codex" / "sessions"
HIDDEN_DISPLAY_EVENT_TYPES = {
    "session_meta",
    "turn_context",
    "task_started",
    "task_complete",
    "turn_aborted",
    "context_compacted",
    "token_count",
    "user_message",
    "turn.started",
    "turn.completed",
    "thread.started",
    "message:developer",
    "message:system",
    "message:user",
    "function_call_output",
    "custom_tool_call_output",
    "web_search_call",
    "web_search_end",
}
CODEX_AGENT_EVENT_TYPE_MAP = {
    "agent_message": "message:assistant",
    "custom_tool_call": "custom_tool_call",
    "exec_command_begin": "exec_command_begin",
    "exec_command_end": "exec_command_end",
    "function_call": "function_call",
    "message:assistant": "message:assistant",
    "patch_apply_end": "patch_apply_end",
    "view_image_tool_call": "view_image_tool_call",
}


def write_state(path: Path, state: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(f"{path.suffix}.tmp")
    tmp.write_text(json.dumps(state, indent=2), encoding="utf-8")
    tmp.replace(path)


def json_dumps(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), default=str)


def find_session_id(value: Any) -> str | None:
    if isinstance(value, dict):
        for key in ("session_id", "conversation_id", "thread_id"):
            found = value.get(key)
            if isinstance(found, str) and found:
                return found
        for item in value.values():
            found = find_session_id(item)
            if found:
                return found
    elif isinstance(value, list):
        for item in value:
            found = find_session_id(item)
            if found:
                return found
    return None


def payload_of(raw: dict) -> dict | None:
    payload = raw.get("payload")
    return payload if isinstance(payload, dict) else None


def timestamp_value(raw: dict) -> float:
    timestamp = raw.get("timestamp")
    if isinstance(timestamp, (int, float)):
        return float(timestamp)
    if isinstance(timestamp, str):
        with_timezone = timestamp.replace("Z", "+00:00")
        try:
            from datetime import datetime

            return datetime.fromisoformat(with_timezone).timestamp()
        except ValueError:
            return time.time()
    return time.time()


def rollout_session_id(path: Path) -> str | None:
    try:
        with path.open("r", encoding="utf-8", errors="replace") as handle:
            for line in handle:
                if not line.strip():
                    continue
                try:
                    raw = json.loads(line)
                except json.JSONDecodeError:
                    continue
                payload = raw.get("payload")
                if raw.get("type") == "session_meta" and isinstance(payload, dict):
                    sid = payload.get("id")
                    return sid if isinstance(sid, str) and sid else None
    except OSError:
        return None
    return None


def find_rollout_for_session(codex_session_id: str | None) -> Path | None:
    if not codex_session_id or not CODEX_ROLLOUT_ROOT.exists():
        return None
    try:
        paths = sorted(CODEX_ROLLOUT_ROOT.rglob("rollout-*.jsonl"), key=lambda item: item.stat().st_mtime, reverse=True)
    except OSError:
        return None
    for path in paths:
        if rollout_session_id(path) == codex_session_id:
            return path
    return None


def display_event_type(raw: dict) -> str:
    payload = raw.get("payload")
    if raw.get("type") in ("event_msg", "response_item") and isinstance(payload, dict):
        if payload.get("type") == "message" and isinstance(payload.get("role"), str):
            return f"message:{payload['role']}"
        payload_type = payload.get("type")
        if isinstance(payload_type, str):
            return payload_type
    if raw.get("type") == "custom_tool_call" and isinstance(raw.get("name"), str):
        return "custom_tool_call"
    item = raw.get("item")
    if isinstance(item, dict) and isinstance(item.get("type"), str):
        return item["type"]
    raw_type = raw.get("type")
    if isinstance(raw_type, str):
        return raw_type
    nested = raw.get("msg")
    if isinstance(nested, dict) and isinstance(nested.get("type"), str):
        return nested["type"]
    return "event"


def content_text(content: Any) -> str:
    if not isinstance(content, list):
        return ""
    parts: list[str] = []
    for item in content:
        if not isinstance(item, dict):
            continue
        text = item.get("text")
        output_text = item.get("output_text")
        if isinstance(text, str):
            parts.append(text)
        elif isinstance(output_text, str):
            parts.append(output_text)
    return "\n".join(parts)


def command_text(command: Any) -> str:
    if isinstance(command, list):
        return " ".join(str(item) for item in command)
    if isinstance(command, str):
        return command
    return ""


def text_from_value(value: Any, depth: int = 0) -> str:
    if depth > 6 or value is None:
        return ""
    if isinstance(value, str):
        return value
    if isinstance(value, (int, float, bool)):
        return str(value)
    if isinstance(value, list):
        return "\n".join(filter(None, (text_from_value(item, depth + 1) for item in value)))
    if not isinstance(value, dict):
        return ""

    if value.get("type") == "event_msg":
        payload = payload_of(value)
        if not payload:
            return ""
        payload_type = payload.get("type") if isinstance(payload.get("type"), str) else ""
        if payload_type == "exec_command_begin":
            command = command_text(payload.get("command"))
            return f"$ {command}" if command else ""
        if payload_type == "exec_command_end":
            command = command_text(payload.get("command"))
            output = payload.get("aggregated_output").strip() if isinstance(payload.get("aggregated_output"), str) else ""
            exit_code = f"exit {payload['exit_code']}" if isinstance(payload.get("exit_code"), int) else ""
            return "\n".join(filter(None, (f"$ {command}".strip(), output, exit_code)))
        if payload_type == "agent_message":
            message = payload.get("message")
            return message if isinstance(message, str) else ""
        if payload_type == "patch_apply_end":
            success = "Applied patch" if payload.get("success") is True else "Patch failed"
            stdout = payload.get("stdout").strip() if isinstance(payload.get("stdout"), str) else ""
            stderr = payload.get("stderr").strip() if isinstance(payload.get("stderr"), str) else ""
            return "\n".join(filter(None, (success, stdout, stderr)))
        if payload_type == "view_image_tool_call" and isinstance(payload.get("path"), str):
            return f"Viewed image: {payload['path']}"

    if value.get("type") == "response_item":
        payload = payload_of(value)
        if not payload:
            return ""
        if payload.get("type") == "message":
            role = payload.get("role") if isinstance(payload.get("role"), str) else ""
            if role != "assistant":
                return ""
            return content_text(payload.get("content"))
        if payload.get("type") == "function_call" and isinstance(payload.get("name"), str):
            if payload.get("name") == "exec_command" and isinstance(payload.get("arguments"), str):
                try:
                    args = json.loads(payload["arguments"])
                except json.JSONDecodeError:
                    return f"Tool call: {payload['name']}"
                if isinstance(args, dict) and isinstance(args.get("cmd"), str):
                    return f"$ {args['cmd']}"
            return f"Tool call: {payload['name']}"
        if payload.get("type") == "custom_tool_call" and isinstance(payload.get("name"), str):
            return "Applied patch" if payload.get("name") == "apply_patch" else f"Tool call: {payload['name']}"
        return ""

    if value.get("type") == "custom_tool_call" and isinstance(value.get("name"), str):
        return "Applied patch" if value.get("name") == "apply_patch" else f"Tool call: {value['name']}"

    for key in ("message", "text", "content", "output", "summary", "final_answer", "item"):
        found = text_from_value(value.get(key), depth + 1)
        if found:
            return found
    return ""


def normalize_message_text(text: str) -> str:
    return text.replace("\r\n", "\n").strip()


def is_assistant_response_item(raw: dict) -> bool:
    payload = payload_of(raw)
    return raw.get("type") == "response_item" and payload is not None and payload.get("type") == "message" and payload.get("role") == "assistant"


def is_duplicate_agent_message(raw: dict, text: str, assistant_response_texts: set[str]) -> bool:
    payload = payload_of(raw)
    return (
        raw.get("type") == "event_msg"
        and payload is not None
        and payload.get("type") == "agent_message"
        and normalize_message_text(text) in assistant_response_texts
    )


def extract_file_changes(raw: dict) -> list[dict]:
    direct = raw if raw.get("type") == "patch_apply_end" else None
    payload = payload_of(raw)
    wrapped = payload if raw.get("type") in ("event_msg", "response_item") and payload and payload.get("type") == "patch_apply_end" else None
    source = direct or wrapped
    if not source:
        return []
    changes = source.get("changes")
    if not isinstance(changes, dict):
        return []
    rows: list[dict] = []
    for path, value in changes.items():
        record = value if isinstance(value, dict) else {}
        rows.append(
            {
                "path": str(path),
                "change_type": record.get("type") if isinstance(record.get("type"), str) else "update",
                "diff": record.get("unified_diff") if isinstance(record.get("unified_diff"), str) else None,
            }
        )
    return rows


def extract_patch_input(raw: dict) -> str | None:
    if raw.get("type") == "custom_tool_call" and raw.get("name") == "apply_patch" and isinstance(raw.get("input"), str):
        return raw["input"]
    payload = payload_of(raw)
    if (
        raw.get("type") == "response_item"
        and payload is not None
        and payload.get("type") == "custom_tool_call"
        and payload.get("name") == "apply_patch"
        and isinstance(payload.get("input"), str)
    ):
        return payload["input"]
    return None


def compact_event(raw: dict, index: int, assistant_response_texts: set[str]) -> dict | None:
    event_type = display_event_type(raw)
    if event_type in HIDDEN_DISPLAY_EVENT_TYPES:
        return None
    text = text_from_value(raw)
    if is_duplicate_agent_message(raw, text, assistant_response_texts):
        return None
    file_changes = extract_file_changes(raw)
    patch_text = extract_patch_input(raw)
    if not text and not file_changes and not patch_text:
        return None
    return {
        "index": index,
        "received_at": timestamp_value(raw),
        "event_type": CODEX_AGENT_EVENT_TYPE_MAP.get(event_type, "operation"),
        "text": text,
        "file_changes": file_changes,
        "patch_text": patch_text,
    }


def raw_identifier(value: Any) -> str | None:
    if isinstance(value, dict):
        for key in ("id", "message_id", "item_id", "call_id", "turn_id"):
            item = value.get(key)
            if isinstance(item, str) and item:
                return item
        found = raw_identifier(value.get("payload"))
        if found:
            return found
        return raw_identifier(value.get("item"))
    return None


def codex_role(raw: dict, compact: dict) -> str:
    payload = raw.get("payload") if isinstance(raw.get("payload"), dict) else {}
    role = payload.get("role")
    if isinstance(role, str):
        return role
    event_type = str(compact.get("event_type") or "")
    if event_type.startswith("message:"):
        return event_type.split(":", 1)[1]
    if event_type in {"tool_result", "tool_call", "function_call", "custom_tool_call"}:
        return "tool"
    return "assistant"


def message_id(provider: str, viewer_session_id: str | None, source_event_id: str | None) -> str:
    value = f"{provider}:{viewer_session_id or ''}:{source_event_id or os.urandom(16).hex()}"
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def insert_provider_message(
    db_path: Path,
    *,
    user_id: str,
    provider: str,
    viewer_session_id: str,
    provider_session_id: str | None,
    query_message_id: str | None,
    driver_run_id: str | None,
    parent_message_id: str | None,
    sender_role_id: str | None,
    recipient_role_id: str | None,
    role_id: str | None,
    event_index: int,
    received_at: float,
    source_path: str | None,
    source_event_id: str,
    source_line: int | None,
    role: str,
    event_type: str,
    text: str,
    raw: dict[str, Any],
    patch_text: str | None = None,
    file_changes: list[dict[str, Any]] | None = None,
) -> None:
    db_path.parent.mkdir(parents=True, exist_ok=True)
    row_id = message_id(provider, viewer_session_id, source_event_id)
    now = time.time()
    with sqlite3.connect(db_path, timeout=10.0) as connection:
        connection.execute("PRAGMA foreign_keys=ON")
        connection.execute(
            """
            INSERT INTO super_workspace_messages (
              id, user_id, conversation_id, parent_message_id, sender_role_id, recipient_role_id, role_id,
              query_message_id, driver_run_id, provider, viewer_session_id, provider_session_id,
              event_index, received_at, source_path, source_event_id, source_line, role, event_type,
              text, query, status, rationale, error, requested_role_ids_json, selected_role_ids_json,
              patch_text, raw_json, occurred_at, ingested_at
            )
            VALUES (?, ?, 'default', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, '', '', '[]', '[]', ?, ?, ?, ?)
            ON CONFLICT(provider, viewer_session_id, source_event_id) DO UPDATE SET
              user_id=excluded.user_id,
              parent_message_id=excluded.parent_message_id,
              sender_role_id=excluded.sender_role_id,
              recipient_role_id=excluded.recipient_role_id,
              role_id=excluded.role_id,
              query_message_id=excluded.query_message_id,
              driver_run_id=excluded.driver_run_id,
              provider_session_id=excluded.provider_session_id,
              event_index=excluded.event_index,
              received_at=excluded.received_at,
              source_path=excluded.source_path,
              source_line=excluded.source_line,
              role=excluded.role,
              event_type=excluded.event_type,
              text=excluded.text,
              patch_text=excluded.patch_text,
              raw_json=excluded.raw_json,
              occurred_at=excluded.occurred_at,
              ingested_at=excluded.ingested_at
            """,
            (
                row_id,
                user_id,
                parent_message_id,
                sender_role_id,
                recipient_role_id,
                role_id,
                query_message_id,
                driver_run_id,
                provider,
                viewer_session_id,
                provider_session_id,
                event_index,
                received_at,
                source_path,
                source_event_id,
                source_line,
                role,
                event_type,
                text,
                patch_text,
                json_dumps(raw),
                received_at,
                now,
            ),
        )
        connection.execute("DELETE FROM super_workspace_message_file_changes WHERE message_id = ?", (row_id,))
        for position, item in enumerate(file_changes or []):
            if not isinstance(item, dict):
                continue
            path = item.get("path")
            change_type = item.get("change_type")
            if not isinstance(path, str) or not isinstance(change_type, str):
                continue
            diff = item.get("diff") if isinstance(item.get("diff"), str) else None
            connection.execute(
                """
                INSERT INTO super_workspace_message_file_changes (message_id, position, path, change_type, diff)
                VALUES (?, ?, ?, ?, ?)
                """,
                (row_id, position, path, change_type, diff),
            )


def record_prompt(
    db_path: Path,
    *,
    user_id: str,
    viewer_session_id: str,
    provider_session_id: str | None,
    lineage: dict[str, str | None],
    prompt: str,
    created_at: float,
) -> None:
    text = prompt.strip()
    if not text:
        return
    source_event_id = f"prompt:{created_at}:{hashlib.sha256(text.encode('utf-8')).hexdigest()[:16]}"
    insert_provider_message(
        db_path,
        user_id=user_id,
        provider="codex",
        viewer_session_id=viewer_session_id,
        provider_session_id=provider_session_id,
        query_message_id=lineage.get("query_message_id"),
        driver_run_id=lineage.get("driver_run_id"),
        parent_message_id=lineage.get("parent_message_id"),
        sender_role_id=lineage.get("sender_role_id"),
        recipient_role_id=lineage.get("recipient_role_id"),
        role_id=lineage.get("role_id"),
        event_index=0,
        received_at=created_at,
        source_path=None,
        source_event_id=source_event_id,
        source_line=None,
        role="user",
        event_type="message:user",
        text=text,
        raw={"type": "codex_prompt", "prompt": {"text": text, "created_at": created_at}},
    )


def read_new_rollout_events(path: Path, line_count: int) -> tuple[list[tuple[int, dict]], int]:
    events: list[tuple[int, dict]] = []
    next_count = line_count
    try:
        with path.open("r", encoding="utf-8", errors="replace") as handle:
            for line_number, line in enumerate(handle):
                if line_number < line_count:
                    continue
                next_count = line_number + 1
                raw_line = line.rstrip("\n")
                if not raw_line.strip():
                    continue
                try:
                    raw = json.loads(raw_line)
                except json.JSONDecodeError:
                    raw = {"type": "invalid_json", "line": raw_line}
                events.append((line_number, raw))
    except OSError:
        return [], line_count
    return events, next_count


def write_rollout_events(db_path: Path, state: dict, rollout_path: Path, new_events: list[tuple[int, dict]]) -> None:
    if not new_events:
        return
    assistant_response_texts = {
        normalize_message_text(text_from_value(raw))
        for _line_number, raw in new_events
        if is_assistant_response_item(raw)
    }
    assistant_response_texts.discard("")
    for line_number, raw in new_events:
        compact = compact_event(raw, line_number, assistant_response_texts)
        if compact is None:
            continue
        source_event_id = raw_identifier(raw) or f"{rollout_path.as_posix()}:{line_number + 1}"
        insert_provider_message(
            db_path,
            user_id=str(state["user_id"]),
            provider="codex",
            viewer_session_id=str(state["viewer_session_id"]),
            provider_session_id=state.get("codex_session_id") if isinstance(state.get("codex_session_id"), str) else None,
            query_message_id=state.get("query_message_id") if isinstance(state.get("query_message_id"), str) else None,
            driver_run_id=state.get("driver_run_id") if isinstance(state.get("driver_run_id"), str) else None,
            parent_message_id=state.get("parent_message_id") if isinstance(state.get("parent_message_id"), str) else None,
            sender_role_id=state.get("sender_role_id") if isinstance(state.get("sender_role_id"), str) else None,
            recipient_role_id=state.get("recipient_role_id") if isinstance(state.get("recipient_role_id"), str) else None,
            role_id=state.get("role_id") if isinstance(state.get("role_id"), str) else None,
            event_index=line_number,
            received_at=float(compact["received_at"]),
            source_path=rollout_path.as_posix(),
            source_event_id=source_event_id,
            source_line=line_number + 1,
            role=codex_role(raw, compact),
            event_type=str(compact.get("event_type") or "operation"),
            text=str(compact.get("text") or ""),
            raw=raw,
            patch_text=compact.get("patch_text") if isinstance(compact.get("patch_text"), str) else None,
            file_changes=compact.get("file_changes") if isinstance(compact.get("file_changes"), list) else [],
        )


def stdout_reader(process: subprocess.Popen, stdout_path: Path, state_path: Path, state: dict, lock: threading.Lock) -> None:
    assert process.stdout is not None
    with stdout_path.open("ab") as stdout:
        while True:
            line = process.stdout.readline()
            if not line:
                return
            stdout.write(line)
            stdout.flush()
            try:
                raw = json.loads(line.decode("utf-8", errors="replace"))
            except json.JSONDecodeError:
                continue
            codex_session_id = find_session_id(raw)
            if not codex_session_id:
                continue
            with lock:
                if state.get("codex_session_id") != codex_session_id:
                    state["codex_session_id"] = codex_session_id
                    state["updated_at"] = time.time()
                    write_state(state_path, state)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--state", required=True)
    parser.add_argument("--stdout", required=True)
    parser.add_argument("--stderr", required=True)
    parser.add_argument("--prompt", required=True)
    parser.add_argument("--cwd", required=True)
    parser.add_argument("--command", required=True)
    parser.add_argument("--history-db", required=True)
    parser.add_argument("--viewer-session-id", required=True)
    parser.add_argument("--user-id", required=True)
    parser.add_argument("--query-message-id")
    parser.add_argument("--driver-run-id")
    parser.add_argument("--parent-message-id")
    parser.add_argument("--sender-role-id")
    parser.add_argument("--recipient-role-id")
    parser.add_argument("--role-id")
    parser.add_argument("--role-name")
    args = parser.parse_args()

    state_path = Path(args.state)
    stdout_path = Path(args.stdout)
    stderr_path = Path(args.stderr)
    prompt_path = Path(args.prompt)
    history_db_path = Path(args.history_db)
    command = json.loads(args.command)
    started_at = time.time()
    lineage = {
        "query_message_id": args.query_message_id,
        "driver_run_id": args.driver_run_id,
        "parent_message_id": args.parent_message_id,
        "sender_role_id": args.sender_role_id,
        "recipient_role_id": args.recipient_role_id,
        "role_id": args.role_id,
        "role_name": args.role_name,
    }
    registry_name = None
    if args.driver_run_id and args.role_id:
        registry_name = driver_process_name(args.role_name, args.role_id, args.driver_run_id)
    state = {
        "runner_pid": os.getpid(),
        "codex_pid": None,
        "codex_session_id": None,
        "rollout_path": None,
        "rollout_line_count": 0,
        "viewer_session_id": args.viewer_session_id,
        "user_id": args.user_id,
        "status": "starting",
        "exit_code": None,
        "started_at": started_at,
        "updated_at": started_at,
        "ended_at": None,
        "command": command,
        "cwd": args.cwd,
        **{key: value for key, value in lineage.items() if value},
    }
    if registry_name:
        slot = process_slot_state(registry_name)
        current_pid = os.getpid()
        if slot["pid_file_exists"] and slot["alive"] and slot["pid"] != current_pid:
            ended_at = time.time()
            state.update({"status": "failed", "exit_code": 1, "updated_at": ended_at, "ended_at": ended_at})
            write_state(state_path, state)
            stderr_path.parent.mkdir(parents=True, exist_ok=True)
            with stderr_path.open("a", encoding="utf-8", errors="replace") as stderr:
                stderr.write(
                    "viewer codex driver refused duplicate start: "
                    f"pid_file={slot['pid_path']} live_pid={slot['pid']} current_pid={current_pid}\n"
                )
            return 1
        if slot["pid_file_exists"] and not slot["alive"]:
            stderr_path.parent.mkdir(parents=True, exist_ok=True)
            with stderr_path.open("a", encoding="utf-8", errors="replace") as stderr:
                stderr.write(
                    "viewer codex driver found stale pid file; overwriting: "
                    f"pid_file={slot['pid_path']} stale_pid={slot['pid']}\n"
                )
    write_state(state_path, state)
    if registry_name:
        write_process_state(
            registry_name,
            os.getpid(),
            {
                "kind": "codex_driver",
                "role_name": args.role_name,
                "role_id": args.role_id,
                "driver_run_id": args.driver_run_id,
                "query_message_id": args.query_message_id,
                "state_path": state_path.as_posix(),
            },
        )
    stdout_path.parent.mkdir(parents=True, exist_ok=True)
    stderr_path.parent.mkdir(parents=True, exist_ok=True)

    try:
        prompt_text = prompt_path.read_text(encoding="utf-8", errors="replace")
        record_prompt(
            history_db_path,
            user_id=args.user_id,
            viewer_session_id=args.viewer_session_id,
            provider_session_id=None,
            lineage=lineage,
            prompt=prompt_text,
            created_at=started_at,
        )
        with prompt_path.open("rb") as prompt, stderr_path.open("ab") as stderr:
            process = subprocess.Popen(command, stdin=prompt, stdout=subprocess.PIPE, stderr=stderr, cwd=args.cwd, env=os.environ.copy())
            state.update({"codex_pid": process.pid, "status": "running", "updated_at": time.time()})
            write_state(state_path, state)
            if registry_name:
                write_process_state(
                    registry_name,
                    os.getpid(),
                    {
                        "kind": "codex_driver",
                        "role_name": args.role_name,
                        "role_id": args.role_id,
                        "driver_run_id": args.driver_run_id,
                        "query_message_id": args.query_message_id,
                        "state_path": state_path.as_posix(),
                        "status": "running",
                        "codex_pid": process.pid,
                    },
                )
            lock = threading.Lock()
            reader = threading.Thread(target=stdout_reader, args=(process, stdout_path, state_path, state, lock), daemon=True)
            reader.start()
            while process.poll() is None:
                with lock:
                    codex_session_id = state.get("codex_session_id") if isinstance(state.get("codex_session_id"), str) else None
                    rollout_path = Path(state["rollout_path"]) if isinstance(state.get("rollout_path"), str) else None
                    line_count = int(state.get("rollout_line_count") or 0)
                if rollout_path is None and codex_session_id:
                    rollout_path = find_rollout_for_session(codex_session_id)
                    if rollout_path is not None:
                        with lock:
                            state["rollout_path"] = rollout_path.as_posix()
                            state["updated_at"] = time.time()
                            write_state(state_path, state)
                if rollout_path is not None:
                    new_events, next_count = read_new_rollout_events(rollout_path, line_count)
                    write_rollout_events(history_db_path, state, rollout_path, new_events)
                    if next_count != line_count:
                        with lock:
                            state["rollout_line_count"] = next_count
                            state["updated_at"] = time.time()
                            write_state(state_path, state)
                time.sleep(0.2)
            exit_code = process.wait()
            reader.join(timeout=1.0)
            with lock:
                rollout_path = Path(state["rollout_path"]) if isinstance(state.get("rollout_path"), str) else None
                line_count = int(state.get("rollout_line_count") or 0)
            if rollout_path is not None:
                new_events, next_count = read_new_rollout_events(rollout_path, line_count)
                write_rollout_events(history_db_path, state, rollout_path, new_events)
                with lock:
                    state["rollout_line_count"] = next_count
    except FileNotFoundError:
        exit_code = 127
    except Exception as exc:
        with stderr_path.open("a", encoding="utf-8", errors="replace") as stderr:
            stderr.write(f"viewer codex driver failed: {exc}\n")
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
    if registry_name:
        write_process_state(
            registry_name,
            os.getpid(),
            {
                "kind": "codex_driver",
                "role_name": args.role_name,
                "role_id": args.role_id,
                "driver_run_id": args.driver_run_id,
                "query_message_id": args.query_message_id,
                "state_path": state_path.as_posix(),
                "status": state["status"],
                "exit_code": exit_code,
            },
        )
    return int(exit_code)


if __name__ == "__main__":
    raise SystemExit(main())
