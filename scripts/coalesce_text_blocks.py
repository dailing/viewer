"""One-shot repair: coalesce fragmented streaming message_blocks (agent_text /
thinking) into one row per contiguous same-kind segment.

Historical ingestion (framework v0.28..v0.32, before per-segment block
aggregation) wrote one message_blocks row per streaming delta, so a turn shows
as dozens of 1-2 char "thinking"/"agent_text" rows in the activity timeline.
The backend now merges deltas at ingest time; this script rewrites the rows
written before that fix.

Merge rule mirrors the live path: within a turn, a maximal run of consecutive
same-kind streaming rows (ordered by occurred_at,rowid = insertion order)
merges into the first row (text concatenated, first row's id/event_id/
occurred_at kept); the rest are deleted. Non-streaming kinds (tool_call,
token_usage, other) are never touched and always break a run.

Safety: turns whose turns.ended_at is NULL (possibly still streaming) are
skipped entirely, so a live merge target row can never be deleted from under
the running backend. Idempotent: after a run merges, its length is 1.

Usage: .venv/bin/python scripts/coalesce_text_blocks.py [--db PATH] [--dry-run]
"""

import argparse
import sqlite3
import sys
from pathlib import Path

STREAMING_KINDS = ("agent_text", "thinking")
DEFAULT_DB = Path.home() / ".local/share/viewer/chat.sqlite3"


def coalesce(db_path: Path, dry_run: bool) -> None:
    con = sqlite3.connect(db_path)
    con.execute("pragma busy_timeout=5000")
    try:
        active_turns = {row[0] for row in con.execute("select id from turns where ended_at is null")}
        rows = con.execute(
            "select rowid, id, turn_id, kind, text from message_blocks order by turn_id, occurred_at, rowid"
        ).fetchall()

        updates: list[tuple[str, int]] = []  # (merged text, first rowid)
        deletes: list[int] = []
        runs = 0
        run_first: tuple[int, str] | None = None  # (rowid, text)
        run_kind: str | None = None
        run_len = 0
        run_turn: str | None = None

        def flush() -> None:
            nonlocal runs, run_first, run_len
            if run_first is not None and run_len > 1:
                runs += 1
                updates.append((run_first[1], run_first[0]))
            run_first, run_len = None, 0

        for rowid, _id, turn_id, kind, text in rows:
            if turn_id in active_turns or kind not in STREAMING_KINDS or kind != run_kind or turn_id != run_turn:
                flush()
                run_kind = None
                if turn_id in active_turns or kind not in STREAMING_KINDS:
                    continue
            if run_first is None:
                run_first, run_kind, run_turn, run_len = (rowid, text), kind, turn_id, 1
            else:
                run_first = (run_first[0], run_first[1] + text)
                run_len += 1
                deletes.append(rowid)
        flush()

        print(f"rows={len(rows)} runs_merged={runs} rows_deleted={len(deletes)} active_turns_skipped={len(active_turns)}")
        if dry_run:
            print("dry run: no changes written")
            return
        con.execute("begin immediate")
        con.executemany("update message_blocks set text=? where rowid=?", updates)
        con.executemany("delete from message_blocks where rowid=?", [(rowid,) for rowid in deletes])
        con.commit()

        # verify: no mergeable run remains outside active turns — must scan ALL
        # kinds, since a non-streaming row breaks a run even though it is not
        # itself mergeable.
        remaining = 0
        prev: tuple[str, str] | None = None
        for turn_id, kind in con.execute("select turn_id, kind from message_blocks order by turn_id, occurred_at, rowid"):
            current = (turn_id, kind) if kind in STREAMING_KINDS else None
            if current is not None and current == prev and turn_id not in active_turns:
                remaining += 1
            prev = current
        if remaining:
            print(f"FAIL: {remaining} mergeable rows remain", file=sys.stderr)
            sys.exit(1)
        print("PASS: history coalesced, no mergeable runs remain")
    finally:
        con.close()


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", type=Path, default=DEFAULT_DB)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    if not args.db.exists():
        print(f"db not found: {args.db}", file=sys.stderr)
        sys.exit(1)
    coalesce(args.db, args.dry_run)


if __name__ == "__main__":
    main()
