#!/usr/bin/env python3
"""Coalesce fragmented assistant text messages in the chat plugin DB.

Pre-aggregation ingestion wrote one `messages` row per streaming delta
(1-2 chars each). The backend now aggregates deltas into one open message
per segment; this script repairs historical rows by merging consecutive
assistant text fragments within a turn, breaking only where a non-agent_text
message block (tool call, file change, ...) occurred between two fragments —
that boundary is a real segment break and must be preserved.

Idempotent: a second run finds nothing to merge.
Usage: coalesce_text_messages.py <chat.sqlite3 path>
"""
import sqlite3
import sys


def coalesce(db_path: str) -> None:
    db = sqlite3.connect(db_path)
    db.execute("PRAGMA journal_mode=WAL")
    rows = db.execute(
        "SELECT id, chat_id, turn_id, text, created_at FROM messages "
        "WHERE role = 'assistant' ORDER BY chat_id, turn_id, created_at, id"
    ).fetchall()
    blocks = db.execute(
        "SELECT turn_id, occurred_at FROM message_blocks WHERE kind != 'agent_text'"
    ).fetchall()
    blocks_by_turn: dict[str, list[int]] = {}
    for turn_id, occurred_at in blocks:
        if occurred_at is not None:
            blocks_by_turn.setdefault(turn_id, []).append(occurred_at)
    for values in blocks_by_turn.values():
        values.sort()

    def blocked_between(turn_id: str, after: int, upto: int) -> bool:
        return any(after < b <= upto for b in blocks_by_turn.get(turn_id, []))

    merged_groups = 0
    removed = 0
    with db:
        group: list[tuple] = []
        prev_key = None

        def flush() -> None:
            nonlocal merged_groups, removed
            if len(group) > 1:
                text = "".join(item[3] for item in group)
                db.execute("UPDATE messages SET text = ? WHERE id = ?", (text, group[0][0]))
                db.executemany("DELETE FROM messages WHERE id = ?", [(item[0],) for item in group[1:]])
                merged_groups += 1
                removed += len(group) - 1
            group.clear()

        for row in rows:
            key = (row[1], row[2])  # chat_id, turn_id
            if key != prev_key:
                flush()
                prev_key = key
            elif group and blocked_between(row[2], group[-1][4], row[4]):
                flush()
            group.append(row)
        flush()

    remaining = db.execute("SELECT count(*) FROM messages WHERE role = 'assistant'").fetchone()[0]
    print(f"merged {merged_groups} groups, removed {removed} fragment rows, {remaining} assistant messages remain")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    coalesce(sys.argv[1])
