#!/usr/bin/env python3
"""Extract agent responses + their user queries from the Viewer DB into
summary_test_inputs/ for summarization experiments.

NOTE on schema: for message:query rows the user's text lives in the `query`
column (`text` is empty by design). Reading `text` yields the old bug where
every input said "(无关联 query 记录)".
"""
from __future__ import annotations

import argparse
import re
import sqlite3
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
DEFAULT_DB = Path.home() / ".view" / "agent-history.sqlite3"


def source_message_ids(input_dir: Path) -> list[str]:
    ids = []
    for f in sorted(input_dir.glob("text_*.md")):
        m = re.search(r"source_message_id: (.+)", f.read_text(encoding="utf-8"))
        if m:
            ids.append(m.group(1).strip())
    return ids


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", default=str(DEFAULT_DB))
    parser.add_argument("--input-dir", default=str(REPO_ROOT / "summary_test_inputs"),
                        help="existing inputs (source ids are re-read from frontmatter)")
    args = parser.parse_args()

    input_dir = Path(args.input_dir)
    ids = source_message_ids(input_dir)
    if not ids:
        raise SystemExit(f"no source_message_id found in {input_dir}")

    db = sqlite3.connect(args.db)
    db.row_factory = sqlite3.Row
    cur = db.cursor()

    for i, sid in enumerate(ids, start=1):
        row = cur.execute(
            "SELECT id, conversation_id, provider, role_id, occurred_at, text, query_message_id "
            "FROM super_workspace_messages WHERE id=?",
            (sid,),
        ).fetchone()
        if not row:
            print(f"SKIP {sid}: not in DB")
            continue

        query_text = ""
        if row["query_message_id"]:
            q = cur.execute(
                # message:query rows store the user text in `query`, not `text`
                "SELECT coalesce(nullif(query, ''), text) AS qt "
                "FROM super_workspace_messages WHERE id=?",
                (row["query_message_id"],),
            ).fetchone()
            query_text = (q["qt"] if q else "") or ""

        occurred = row["occurred_at"] or ""
        import datetime
        try:
            occurred = datetime.datetime.fromtimestamp(float(occurred)).strftime("%Y-%m-%d %H:%M")
        except (ValueError, TypeError):
            pass

        out = (
            f"---\nsource_message_id: {row['id']}\nchat_id: {row['conversation_id']}\n"
            f"provider: {row['provider']}\nrole_id: {row['role_id']}\n"
            f"occurred_at: {occurred}\nchars: {len(row['text'] or '')}\n---\n\n"
            f"# User Query\n\n{query_text or '(无关联 query 记录)'}\n\n"
            f"# Agent Response\n\n{row['text'] or ''}\n"
        )
        path = input_dir / f"text_{i:03d}.md"
        path.write_text(out, encoding="utf-8")
        print(f"OK {path.name} query_chars={len(query_text)} response_chars={len(row['text'] or '')}")


if __name__ == "__main__":
    main()
