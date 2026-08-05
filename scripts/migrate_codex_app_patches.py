#!/usr/bin/env python3
"""Backfill Codex App Server patch events from Codex rollout JSONL files."""

from __future__ import annotations

import argparse
import json
import sys
from collections import Counter
from datetime import datetime
from pathlib import Path
from typing import Any

from sqlalchemy import select


PROJECT_ROOT = Path(__file__).resolve().parents[1]
if PROJECT_ROOT.as_posix() not in sys.path:
    sys.path.insert(0, PROJECT_ROOT.as_posix())

from backend.app.agent_history import SuperWorkspaceMessageRow, agent_history_store  # noqa: E402


MIGRATION_ID = "codex_app_rollout_patches_v1"


def parse_timestamp(value: Any) -> float | None:
    if not isinstance(value, str) or not value:
        return None
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()
    except ValueError:
        return None


def raw_turn_id(raw_json: str) -> str:
    try:
        raw = json.loads(raw_json or "{}")
    except (TypeError, json.JSONDecodeError):
        return ""
    params = raw.get("params") if isinstance(raw.get("params"), dict) else {}
    turn = params.get("turn") if isinstance(params.get("turn"), dict) else {}
    return str(params.get("turnId") or turn.get("id") or raw.get("codex_turn_id") or "")


def rollout_session_id(path: Path) -> str:
    try:
        with path.open(encoding="utf-8") as handle:
            first = json.loads(next(handle))
    except (OSError, StopIteration, json.JSONDecodeError):
        return ""
    payload = first.get("payload") if isinstance(first.get("payload"), dict) else {}
    return str(payload.get("id") or "") if first.get("type") == "session_meta" else ""


def normalized_file_changes(payload: dict[str, Any]) -> list[dict[str, Any]]:
    changes = payload.get("changes") if isinstance(payload.get("changes"), dict) else {}
    normalized: list[dict[str, Any]] = []
    for path, value in changes.items():
        if not isinstance(path, str) or not isinstance(value, dict):
            continue
        diff = value.get("unified_diff")
        normalized.append(
            {
                "path": path,
                "change_type": str(value.get("type") or "update"),
                "diff": diff if isinstance(diff, str) else None,
            }
        )
    return normalized


def combined_patch_text(file_changes: list[dict[str, Any]]) -> str:
    blocks = []
    for change in file_changes:
        diff = change.get("diff")
        if isinstance(diff, str) and diff:
            blocks.append(f"# {change['path']}\n{diff}")
    return "\n\n".join(blocks)


def lineage_index() -> tuple[dict[tuple[str, str], SuperWorkspaceMessageRow], set[tuple[str, str]], set[str]]:
    lineage: dict[tuple[str, str], SuperWorkspaceMessageRow] = {}
    live_patch_turns: set[tuple[str, str]] = set()
    existing_ids: set[str] = set()
    with agent_history_store.read_session() as db:
        rows = list(
            db.scalars(
                select(SuperWorkspaceMessageRow).where(
                    SuperWorkspaceMessageRow.provider == "codex-app-server",
                )
            ).all()
        )
    for row in rows:
        existing_ids.add(str(row.id))
        provider_session_id = str(row.provider_session_id or "")
        codex_turn_id = raw_turn_id(row.raw_json)
        if not provider_session_id or not codex_turn_id:
            continue
        lineage[(provider_session_id, codex_turn_id)] = row
        if row.event_type != "patch_apply_end":
            continue
        try:
            raw = json.loads(row.raw_json or "{}")
        except (TypeError, json.JSONDecodeError):
            raw = {}
        if raw.get("migration_id") != MIGRATION_ID:
            live_patch_turns.add((provider_session_id, codex_turn_id))
    return lineage, live_patch_turns, existing_ids


def migrate(rollout_root: Path, apply: bool) -> Counter[str]:
    lineage, live_patch_turns, existing_ids = lineage_index()
    stats: Counter[str] = Counter()
    for path in sorted(rollout_root.rglob("rollout-*.jsonl")):
        stats["files_scanned"] += 1
        provider_session_id = rollout_session_id(path)
        if not provider_session_id:
            stats["invalid_files"] += 1
            continue
        try:
            handle = path.open(encoding="utf-8")
        except OSError:
            stats["invalid_files"] += 1
            continue
        with handle:
            for source_line, line in enumerate(handle, start=1):
                try:
                    record = json.loads(line)
                except json.JSONDecodeError:
                    stats["invalid_lines"] += 1
                    continue
                payload = record.get("payload") if isinstance(record.get("payload"), dict) else {}
                if payload.get("type") != "patch_apply_end" or payload.get("success") is not True:
                    continue
                stats["patch_events"] += 1
                codex_turn_id = str(payload.get("turn_id") or "")
                key = (provider_session_id, codex_turn_id)
                matched = lineage.get(key)
                if matched is None:
                    stats["unmapped"] += 1
                    continue
                if key in live_patch_turns:
                    stats["live_turn_skipped"] += 1
                    continue
                file_changes = normalized_file_changes(payload)
                if not file_changes:
                    stats["empty_changes"] += 1
                    continue
                call_id = str(payload.get("call_id") or source_line)
                source_event_id = f"codex-app-server:{provider_session_id}:{codex_turn_id}:rollout-patch:{call_id}"
                row_id = f"codex-app-server:{matched.viewer_session_id}:{source_event_id}"
                existed = row_id in existing_ids
                stats["already_migrated" if existed else "ready"] += 1
                if not apply:
                    continue
                occurred_at = parse_timestamp(record.get("timestamp")) or float(matched.occurred_at)
                paths = [change["path"] for change in file_changes]
                raw = {
                    "migration_id": MIGRATION_ID,
                    "chat_id": str(matched.conversation_id),
                    "codex_turn_id": codex_turn_id,
                    "rollout_path": path.as_posix(),
                    "rollout_line": source_line,
                    "payload": payload,
                }
                agent_history_store.record_provider_message(
                    user_id=str(matched.user_id),
                    workspace_id=str(matched.workspace_id),
                    turn_id=str(matched.turn_id),
                    provider="codex-app-server",
                    viewer_session_id=str(matched.viewer_session_id),
                    provider_session_id=provider_session_id,
                    query_message_id=matched.query_message_id,
                    driver_run_id=matched.driver_run_id,
                    parent_message_id=matched.parent_message_id,
                    sender_role_id=matched.sender_role_id,
                    recipient_role_id=matched.recipient_role_id,
                    role_id=matched.role_id,
                    event_index=1_000_000 + source_line,
                    received_at=occurred_at,
                    source_path=path.as_posix(),
                    source_event_id=source_event_id,
                    source_line=source_line,
                    role="assistant",
                    event_type="patch_apply_end",
                    text=f"File edits: {', '.join(paths)}",
                    raw=raw,
                    patch_text=combined_patch_text(file_changes),
                    file_changes=file_changes,
                )
                existing_ids.add(row_id)
                stats["updated" if existed else "inserted"] += 1
    return stats


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--rollout-root",
        type=Path,
        default=Path.home() / ".codex" / "sessions",
        help="Root containing Codex rollout JSONL files",
    )
    parser.add_argument("--apply", action="store_true", help="Write the backfill; without this flag only report a dry run")
    args = parser.parse_args()
    if not args.rollout_root.is_dir():
        parser.error(f"rollout root does not exist: {args.rollout_root}")
    stats = migrate(args.rollout_root, args.apply)
    print(json.dumps({"mode": "apply" if args.apply else "dry-run", **dict(sorted(stats.items()))}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
