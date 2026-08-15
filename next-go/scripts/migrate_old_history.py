#!/usr/bin/env python3
"""One-shot history migration: old viewer (~/.view/agent-history.sqlite3) -> new
viewer chat store (chat.sqlite3).

Copies chats (missing only), visible messages, tool/thinking activity rows (as
message_blocks), and per-turn rows. Raw provider events stay in the old DB
(archive bulk; the new store records its own going forward). Idempotent via
INSERT OR IGNORE on preserved old ids. Old DB is opened read-only.

Usage: migrate_old_history.py [old_db] [new_db]
"""
import json
import sqlite3
import sys

OLD_DB = sys.argv[1] if len(sys.argv) > 1 else "/mnt/oldroot/home/d/.view/agent-history.sqlite3"
NEW_DB = sys.argv[2] if len(sys.argv) > 2 else "/tmp/viewerd-live/chat.sqlite3"
VIEWER_ROOT = "/mnt/oldroot/home/d/Sync"

# old event_type -> new block kind (None means: becomes a timeline message)
KIND_MAP = {
    "reasoning": "thinking",
    "tool_call": "tool_call",
    "function_call": "tool_call",
    "custom_tool_call": "tool_call",
    "tool_result": "tool_result",
    "patch_apply_end": "file_change",
    "session_update": "other",
    "plan_update": "other",
}
USER_EVENTS = {"message:query", "message:user"}
ASSISTANT_EVENT = "message:assistant"


def ms(seconds: float) -> int:
    return int(seconds * 1000)


def main() -> None:
    old = sqlite3.connect(f"file:{OLD_DB}?mode=ro", uri=True)
    new = sqlite3.connect(NEW_DB)
    new.execute("pragma journal_mode(WAL)")
    new.execute("pragma busy_timeout(5000)")

    role_names = dict(old.execute("select id, name from super_workspace_roles").fetchall())
    turn_role_names = dict(
        old.execute("select turn_id, role_name from super_workspace_turn_summaries where role_name is not null and role_name != ''").fetchall()
    )

    # --- chats: insert only those missing in the new store -----------------
    existing_chats = {row[0] for row in new.execute("select id from chats")}
    pinned = {row[0] for row in old.execute("select chat_id from super_workspace_chat_pins")}
    chats_added = 0
    for row in old.execute(
        "select id, name, type, common_prompt, member_role_ids_json, root, role_routing_policy_overrides_json, created_at, updated_at from super_workspace_chats"
    ):
        chat_id, name, ctype, prompt, members, root, overrides, created, updated = row
        if chat_id in existing_chats:
            continue
        root = root if root.startswith("/") else (VIEWER_ROOT if root == "." else f"{VIEWER_ROOT}/{root}")
        new.execute(
            "insert or ignore into chats (id, name, type, pinned, root, common_prompt, member_role_ids, role_routing_policy_overrides, created_at, updated_at)"
            " values (?,?,?,?,?,?,?,?,?,?)",
            (chat_id, name, ctype, 1 if chat_id in pinned else 0, root, prompt or "", members or "[]", overrides or "{}", ms(created), ms(updated)),
        )
        chats_added += 1
    new.executemany("update chats set pinned=1 where id=?", [(c,) for c in pinned])

    # --- messages, blocks, turns -------------------------------------------
    stats = {"user": 0, "assistant": 0, "blocks": 0}
    turns: dict[str, dict] = {}
    batch_messages, batch_blocks = [], []

    rows = old.execute(
        "select id, conversation_id, turn_id, role, event_type, text, role_id, occurred_at "
        "from super_workspace_messages order by occurred_at"
    )
    for msg_id, chat_id, turn_id, role, event_type, text, role_id, occurred_at in rows:
        ts = ms(occurred_at)
        turn_id = turn_id or msg_id
        if event_type in USER_EVENTS:
            batch_messages.append((msg_id, chat_id, turn_id, "user", text or "", "user", "", "", ts))
            stats["user"] += 1
            continue
        role_name = turn_role_names.get(turn_id) or role_names.get(role_id or "", "")
        if event_type == ASSISTANT_EVENT:
            batch_messages.append((msg_id, chat_id, turn_id, "assistant", text or "", "role", role_id or "", role_name, ts))
            stats["assistant"] += 1
        else:
            kind = KIND_MAP.get(event_type, "other")
            batch_blocks.append((msg_id, "", chat_id, turn_id, kind, text or "", "{}", ts))
            stats["blocks"] += 1
        turn = turns.setdefault(turn_id, {"chat_id": chat_id, "role_id": role_id or "", "role_name": role_name, "start": ts, "end": ts})
        turn["start"] = min(turn["start"], ts)
        turn["end"] = max(turn["end"], ts)
        if not turn["role_id"] and role_id:
            turn["role_id"], turn["role_name"] = role_id, role_name

    new.executemany("insert or ignore into messages (id, chat_id, turn_id, role, text, sender_from, role_id, role_name, created_at) values (?,?,?,?,?,?,?,?,?)", batch_messages)
    new.executemany("insert or ignore into message_blocks (id, event_id, chat_id, turn_id, kind, text, payload, occurred_at) values (?,?,?,?,?,?,?,?)", batch_blocks)
    new.executemany(
        "insert or ignore into turns (id, chat_id, role_id, role_name, started_at, ended_at, stop_reason) values (?,?,?,?,?,?,null)",
        [(tid, t["chat_id"], t["role_id"], t["role_name"], t["start"], t["end"]) for tid, t in turns.items()],
    )
    new.commit()

    # --- verification summary ----------------------------------------------
    print(f"chats added: {chats_added} (pinned synced: {len(pinned)})")
    print(f"messages: user={stats['user']} assistant={stats['assistant']}, blocks={stats['blocks']}, turns={len(turns)}")
    for table in ["chats", "messages", "message_blocks", "turns"]:
        print(f"new store {table}: {new.execute(f'select count(*) from {table}').fetchone()[0]}")
    old.close()
    new.close()


if __name__ == "__main__":
    main()
