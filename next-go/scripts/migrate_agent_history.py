#!/usr/bin/env python3
"""One-shot Viewer agent-history.sqlite3 to viewer-chat chat.sqlite3 migration.

The source is opened with SQLite's read-only URI mode and copied with the
stdlib online-backup API to a temporary database before any mapping queries.
This cooperates with a live WAL writer and never writes the production source.
Chats already present in the destination are skipped atomically, making reruns
safe. Normalized message rows are also copied into append-only turn_events;
message_blocks are intentionally not migrated because they can be re-parsed
from raw_json. Worker/lease/checkpoint/health tables remain ignored.
"""
from __future__ import annotations

import argparse
import json
import sqlite3
import tempfile
from pathlib import Path
from urllib.parse import quote


TARGET_SCHEMA = """
CREATE TABLE IF NOT EXISTS chats (
 id TEXT PRIMARY KEY, name TEXT, type TEXT, pinned INTEGER NOT NULL DEFAULT 0,
 root TEXT, common_prompt TEXT, member_role_ids TEXT,
 role_routing_policy_overrides TEXT, created_at INTEGER, updated_at INTEGER,
 title TEXT, provider TEXT, provider_profile TEXT, provider_session_id TEXT, cwd TEXT
);
CREATE TABLE IF NOT EXISTS messages (
 id TEXT PRIMARY KEY, chat_id TEXT NOT NULL, turn_id TEXT NOT NULL, role TEXT NOT NULL,
 text TEXT NOT NULL, sender_from TEXT, role_id TEXT, role_name TEXT, created_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_messages_chat_id ON messages(chat_id);
CREATE INDEX IF NOT EXISTS idx_messages_turn_id ON messages(turn_id);
CREATE TABLE IF NOT EXISTS turn_events (
 id TEXT PRIMARY KEY, chat_id TEXT NOT NULL, turn_id TEXT NOT NULL, role_id TEXT,
 provider TEXT NOT NULL, session_id TEXT NOT NULL, seq INTEGER NOT NULL,
 kind TEXT NOT NULL, raw_json TEXT NOT NULL, occurred_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_turn_events_chat_id ON turn_events(chat_id);
CREATE INDEX IF NOT EXISTS idx_turn_events_turn_id ON turn_events(turn_id);
CREATE INDEX IF NOT EXISTS idx_turn_events_role_id ON turn_events(role_id);
CREATE INDEX IF NOT EXISTS idx_turn_events_occurred_at ON turn_events(occurred_at);
CREATE TABLE IF NOT EXISTS turn_summaries (
 turn_id TEXT PRIMARY KEY, chat_id TEXT NOT NULL, role_id TEXT, role_name TEXT,
 provider TEXT, status TEXT, summary TEXT, model TEXT, profile_id TEXT,
 source_message_count INTEGER, source_char_count INTEGER, latency_ms INTEGER,
 error TEXT, occurred_at INTEGER, created_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_turn_summaries_chat_id ON turn_summaries(chat_id);
CREATE INDEX IF NOT EXISTS idx_turn_summaries_role_id ON turn_summaries(role_id);
CREATE INDEX IF NOT EXISTS idx_turn_summaries_occurred_at ON turn_summaries(occurred_at);
CREATE TABLE IF NOT EXISTS role_sessions (
 chat_id TEXT NOT NULL, role_id TEXT NOT NULL, provider TEXT,
 provider_profile TEXT, provider_session_id TEXT, cwd TEXT, updated_at INTEGER,
 PRIMARY KEY(chat_id, role_id)
);
"""


def millis(value: object) -> int:
    try:
        number = float(value or 0)
    except (TypeError, ValueError):
        return 0
    return int(number if number > 10_000_000_000 else number * 1000)


def columns(connection: sqlite3.Connection, table: str) -> set[str]:
    return {str(row[1]) for row in connection.execute(f"PRAGMA table_info({table})")}


def value(row: sqlite3.Row, names: set[str], *candidates: str, default: object = "") -> object:
    for name in candidates:
        if name in names and row[name] is not None:
            return row[name]
    return default


def backup_readonly(source: Path, copy_path: Path) -> None:
    uri = f"file:{quote(str(source.resolve()))}?mode=ro"
    with sqlite3.connect(uri, uri=True, timeout=30) as source_db, sqlite3.connect(copy_path) as copy_db:
        source_db.execute("PRAGMA busy_timeout=30000")
        source_db.backup(copy_db)


def migrate(source: Path, target: Path) -> dict[str, int]:
    if not source.is_file():
        raise FileNotFoundError(source)
    target.parent.mkdir(parents=True, exist_ok=True)
    counts = {"chats": 0, "skipped_chats": 0, "messages": 0, "turn_summaries": 0, "role_sessions": 0, "turn_events": 0}
    with tempfile.TemporaryDirectory(prefix="viewer-chat-migration-") as temporary:
        snapshot = Path(temporary) / "agent-history.snapshot.sqlite3"
        backup_readonly(source, snapshot)
        old = sqlite3.connect(snapshot); old.row_factory = sqlite3.Row
        new = sqlite3.connect(target)
        try:
            new.executescript(TARGET_SCHEMA)
            chat_cols = columns(old, "super_workspace_chats")
            message_cols = columns(old, "super_workspace_messages")
            summary_cols = columns(old, "super_workspace_turn_summaries")
            session_cols = columns(old, "super_workspace_chat_role_sessions")
            role_names = {str(row[0]): str(row[1]) for row in old.execute("SELECT id, name FROM super_workspace_roles")}
            pins = {str(row[0]) for row in old.execute("SELECT chat_id FROM super_workspace_chat_pins")}
            for chat in old.execute("SELECT * FROM super_workspace_chats ORDER BY created_at, id"):
                chat_id = str(value(chat, chat_cols, "id"))
                if new.execute("SELECT 1 FROM chats WHERE id=?", (chat_id,)).fetchone():
                    counts["skipped_chats"] += 1
                    continue
                with new:
                    new.execute(
                        "INSERT INTO chats(id,name,type,pinned,root,common_prompt,member_role_ids,role_routing_policy_overrides,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)",
                        (chat_id, value(chat,chat_cols,"name"), value(chat,chat_cols,"type",default="group"), int(chat_id in pins), value(chat,chat_cols,"root"), value(chat,chat_cols,"common_prompt"), value(chat,chat_cols,"member_role_ids_json","member_role_ids",default="[]"), value(chat,chat_cols,"role_routing_policy_overrides_json","role_routing_policy_overrides",default="{}"), millis(value(chat,chat_cols,"created_at")), millis(value(chat,chat_cols,"updated_at"))),
                    )
                    for message in old.execute("SELECT * FROM super_workspace_messages WHERE conversation_id=? ORDER BY occurred_at,id", (chat_id,)):
                        text = str(value(message,message_cols,"query","text",default=""))
                        if not text.strip():
                            continue
                        raw_role = str(value(message,message_cols,"role",default="assistant"))
                        is_user = bool(str(value(message,message_cols,"query",default="")).strip()) or raw_role == "user"
                        role_id = str(value(message,message_cols,"role_id","sender_role_id",default=""))
                        new.execute("INSERT OR IGNORE INTO messages(id,chat_id,turn_id,role,text,sender_from,role_id,role_name,created_at) VALUES(?,?,?,?,?,?,?,?,?)", (str(value(message,message_cols,"id")),chat_id,str(value(message,message_cols,"turn_id","id")),"user" if is_user else "assistant",text,"user" if is_user else "role",role_id,role_names.get(role_id,""),millis(value(message,message_cols,"occurred_at","received_at"))))
                        counts["messages"] += 1
                    for item in old.execute("SELECT * FROM super_workspace_turn_summaries WHERE conversation_id=? ORDER BY occurred_at,turn_id", (chat_id,)):
                        args=(str(value(item,summary_cols,"turn_id")),chat_id,str(value(item,summary_cols,"role_id")),str(value(item,summary_cols,"role_name")),str(value(item,summary_cols,"provider")),str(value(item,summary_cols,"status",default="completed")),str(value(item,summary_cols,"summary")),str(value(item,summary_cols,"model")),str(value(item,summary_cols,"profile_id")),int(value(item,summary_cols,"source_message_count",default=0)),int(value(item,summary_cols,"source_char_count",default=0)),int(value(item,summary_cols,"latency_ms",default=0)),str(value(item,summary_cols,"error")),millis(value(item,summary_cols,"occurred_at")),millis(value(item,summary_cols,"created_at")))
                        new.execute("INSERT OR IGNORE INTO turn_summaries(turn_id,chat_id,role_id,role_name,provider,status,summary,model,profile_id,source_message_count,source_char_count,latency_ms,error,occurred_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",args);counts["turn_summaries"]+=1
                    for session in old.execute("SELECT * FROM super_workspace_chat_role_sessions WHERE chat_id=?",(chat_id,)):
                        provider=str(value(session,session_cols,"provider")); profile=str(value(session,session_cols,"model",default="")); provider_session=str(value(session,session_cols,"provider_session_id","session_ref","viewer_session_id",default=""))
                        new.execute("INSERT OR REPLACE INTO role_sessions(chat_id,role_id,provider,provider_profile,provider_session_id,cwd,updated_at) VALUES(?,?,?,?,?,?,?)",(chat_id,str(value(session,session_cols,"role_id")),provider,profile,provider_session,str(value(session,session_cols,"cwd")),millis(value(session,session_cols,"updated_at"))));counts["role_sessions"]+=1
                counts["chats"] += 1
            # Every production normalized event retains its original raw_json.
            # Keep this outside the per-chat skip path so upgrading an already
            # migrated M6c database still backfills raw events. INSERT OR IGNORE
            # makes the production message id the idempotency key.
            with new:
                for message in old.execute("SELECT * FROM super_workspace_messages ORDER BY occurred_at,event_index,id"):
                    chat_id = str(value(message, message_cols, "conversation_id"))
                    if not new.execute("SELECT 1 FROM chats WHERE id=?", (chat_id,)).fetchone():
                        continue
                    raw_json = str(value(message, message_cols, "raw_json", default="{}"))
                    cursor = new.execute(
                        "INSERT OR IGNORE INTO turn_events(id,chat_id,turn_id,role_id,provider,session_id,seq,kind,raw_json,occurred_at) VALUES(?,?,?,?,?,?,?,?,?,?)",
                        (
                            str(value(message, message_cols, "id")), chat_id,
                            str(value(message, message_cols, "turn_id", "id")),
                            str(value(message, message_cols, "role_id", "sender_role_id")),
                            str(value(message, message_cols, "provider", default="unknown")),
                            str(value(message, message_cols, "provider_session_id", "viewer_session_id")),
                            int(value(message, message_cols, "event_index", default=0)),
                            str(value(message, message_cols, "event_type", default="unknown")),
                            raw_json, millis(value(message, message_cols, "occurred_at", "received_at")),
                        ),
                    )
                    counts["turn_events"] += max(cursor.rowcount, 0)
        finally:
            old.close(); new.close()
    return counts


def create_fixture(path: Path) -> None:
    db=sqlite3.connect(path)
    db.executescript("""
CREATE TABLE super_workspace_chats(id TEXT,workspace_id TEXT,user_id TEXT,name TEXT,type TEXT,root TEXT,common_prompt TEXT,member_role_ids_json TEXT,role_routing_policy_overrides_json TEXT,created_at REAL,updated_at REAL);
CREATE TABLE super_workspace_chat_pins(user_id TEXT,workspace_id TEXT,chat_id TEXT,created_at REAL,updated_at REAL);
CREATE TABLE super_workspace_roles(id TEXT,name TEXT);
CREATE TABLE super_workspace_messages(id TEXT,turn_id TEXT,conversation_id TEXT,role_id TEXT,sender_role_id TEXT,provider TEXT,viewer_session_id TEXT,provider_session_id TEXT,event_index INTEGER,event_type TEXT,role TEXT,text TEXT,query TEXT,raw_json TEXT,occurred_at REAL,received_at REAL);
CREATE TABLE super_workspace_turn_summaries(turn_id TEXT,conversation_id TEXT,role_id TEXT,role_name TEXT,provider TEXT,status TEXT,summary TEXT,model TEXT,profile_id TEXT,source_message_count INTEGER,source_char_count INTEGER,latency_ms INTEGER,error TEXT,occurred_at REAL,created_at REAL);
CREATE TABLE super_workspace_chat_role_sessions(chat_id TEXT,role_id TEXT,provider TEXT,provider_session_id TEXT,session_ref TEXT,viewer_session_id TEXT,cwd TEXT,model TEXT,updated_at REAL);
CREATE TABLE super_workspace_driver_runs(id TEXT);
""")
    db.execute("INSERT INTO super_workspace_chats VALUES(?,?,?,?,?,?,?,?,?,?,?)",("chat-1","default","dailing","Fixture","group","/tmp","common",'["role-1"]','{"role-1":"route"}',1.0,2.0));db.execute("INSERT INTO super_workspace_chat_pins VALUES(?,?,?,?,?)",("dailing","default","chat-1",1.0,2.0));db.execute("INSERT INTO super_workspace_roles VALUES(?,?)",("role-1","Builder"));db.execute("INSERT INTO super_workspace_messages VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",("user-1","dispatch-1","chat-1",None,None,"viewer","viewer-user","",0,"message:query","user","","hello",'{"query":"hello"}',3.0,3.0));db.execute("INSERT INTO super_workspace_messages VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",("answer-1","turn-1","chat-1","role-1",None,"hermes","viewer-session","provider-session",7,"message:assistant","assistant","done",None,'{"sessionId":"provider-session","update":{"sessionUpdate":"agent_message_chunk","content":{"text":"done"}}}',4.0,4.0));db.execute("INSERT INTO super_workspace_turn_summaries VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",("turn-1","chat-1","role-1","Builder","hermes","completed","summary","model","profile",2,9,12,"",4.0,5.0));db.execute("INSERT INTO super_workspace_chat_role_sessions VALUES(?,?,?,?,?,?,?,?,?)",("chat-1","role-1","hermes","provider-session","session-ref","viewer-session","/tmp","model",6.0));db.execute("INSERT INTO super_workspace_driver_runs VALUES(?)",("ignored",));db.commit();db.close()


def self_test() -> None:
    with tempfile.TemporaryDirectory(prefix="viewer-chat-migration-test-") as temporary:
        root=Path(temporary);source=root/"source.sqlite3";target=root/"chat.sqlite3";create_fixture(source)
        first=migrate(source,target);second=migrate(source,target)
        assert first=={"chats":1,"skipped_chats":0,"messages":2,"turn_summaries":1,"role_sessions":1,"turn_events":2},first
        assert second["chats"]==0 and second["skipped_chats"]==1 and second["turn_events"]==0,second
        db=sqlite3.connect(target)
        assert db.execute("SELECT name,pinned,root,common_prompt,member_role_ids FROM chats").fetchone()==("Fixture",1,"/tmp","common",'["role-1"]')
        assert db.execute("SELECT role,sender_from,text,role_name FROM messages ORDER BY created_at").fetchall()==[("user","user","hello",""),("assistant","role","done","Builder")]
        assert db.execute("SELECT summary,status FROM turn_summaries").fetchone()==("summary","completed")
        assert db.execute("SELECT provider,provider_session_id,cwd FROM role_sessions").fetchone()==("hermes","provider-session","/tmp")
        assert db.execute("SELECT provider,session_id,seq,kind,raw_json FROM turn_events WHERE id='answer-1'").fetchone()==("hermes","provider-session",7,"message:assistant",'{"sessionId":"provider-session","update":{"sessionUpdate":"agent_message_chunk","content":{"text":"done"}}}')
        assert db.execute("SELECT count(*) FROM turn_events").fetchone()[0]==2
        assert not db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='super_workspace_driver_runs'").fetchone()
        db.close();print("migration fixture self-test: PASS",json.dumps(first,sort_keys=True))


def main() -> None:
    parser=argparse.ArgumentParser();parser.add_argument("--source",type=Path,default=Path.home()/".view"/"agent-history.sqlite3");parser.add_argument("--target",type=Path,default=Path.home()/".view"/"chat.sqlite3");parser.add_argument("--self-test",action="store_true");args=parser.parse_args()
    if args.self_test:self_test();return
    print(json.dumps(migrate(args.source,args.target),sort_keys=True))


if __name__ == "__main__":
    main()
