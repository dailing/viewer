#!/usr/bin/env python3
"""Offline Chat-level-only migration against a disposable legacy database."""
from __future__ import annotations
import hashlib, os, sqlite3, subprocess, tempfile
from pathlib import Path

def digest(path: Path) -> str: return hashlib.sha256(path.read_bytes()).hexdigest()
def main() -> None:
    binary = Path(os.environ["VIEWER_CHAT_MIGRATE_BIN"])
    with tempfile.TemporaryDirectory(prefix="viewer-chat-migration-smoke-") as temp:
        data = Path(temp); source = data / "chat.sqlite3"
        database = sqlite3.connect(source)
        statements = [
            "CREATE TABLE chats (id TEXT PRIMARY KEY, name TEXT, type TEXT, pinned BOOLEAN, root TEXT, common_prompt TEXT, member_role_ids TEXT, role_routing_policy_overrides TEXT, provider TEXT, provider_session_id TEXT, created_at INTEGER, updated_at INTEGER)",
            "CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT, description TEXT, prompt TEXT, cwd TEXT, routing_policy_id TEXT, session_policy TEXT, context_recycle_percent REAL, context_recycle_tokens INTEGER, created_at INTEGER, updated_at INTEGER)",
            "CREATE TABLE routing_policies (id TEXT PRIMARY KEY, name TEXT, candidates_json TEXT, auto_failover BOOLEAN, max_attempts INTEGER, created_at INTEGER, updated_at INTEGER)",
            "CREATE TABLE plugin_states (key TEXT PRIMARY KEY, value TEXT)",
            "CREATE TABLE branches (id TEXT PRIMARY KEY, chat_id TEXT, name TEXT)",
            "CREATE TABLE messages (id TEXT PRIMARY KEY, chat_id TEXT, branch_id TEXT, text TEXT)",
            "INSERT INTO chats VALUES ('chat-a','Project','project',1,'/srv/project','project prompt','[\"role-a\"]','{\"role-a\":\"policy-a\"}','legacy-provider','legacy-session',10,20)",
            "INSERT INTO roles VALUES ('role-a','Researcher','research','role prompt','/srv/project','policy-a','reuse',80,100000,11,21)",
            "INSERT INTO routing_policies VALUES ('policy-a','Default','[{\"agent\":\"hermes\",\"provider\":\"kimi\",\"model\":\"k3\"}]',1,2,12,22)",
            "INSERT INTO plugin_states VALUES ('active_chat_id','chat-a')",
            "INSERT INTO plugin_states VALUES ('default_routing_policy_id','policy-a')",
            "INSERT INTO branches VALUES ('old-branch','chat-a','old')",
            "INSERT INTO messages VALUES ('old-message','chat-a','old-branch','must not migrate')",
        ]
        for statement in statements: database.execute(statement)
        database.commit(); database.close()
        before = digest(source)
        result = subprocess.run([str(binary), "--data-dir", str(data), "--yes"], text=True, capture_output=True, timeout=30)
        assert result.returncode == 0, result.stderr
        backup = data / "chat.pre-task-first.sqlite3"
        assert backup.exists() and digest(backup) == before and digest(source) != before
        migrated = sqlite3.connect(source)
        try:
            chat = migrated.execute("select id,name,root,common_prompt,member_role_ids,role_routing_policy_overrides from chats").fetchone()
            assert chat == ("chat-a", "Project", "/srv/project", "project prompt", '["role-a"]', '{"role-a":"policy-a"}')
            assert migrated.execute("select name,cwd,routing_policy_id from roles").fetchone() == ("Researcher", "/srv/project", "policy-a")
            assert migrated.execute("select count(*) from routing_policies where id='policy-a'").fetchone()[0] == 1
            task = migrated.execute("select chat_id,title,title_source,ended_at,pinned from tasks").fetchone()
            assert task == ("chat-a", "主线", "default", None, 1)
            for table in ["messages", "turns", "turn_summaries", "task_summaries", "role_sessions", "chat_drafts", "decisions", "context_builds"]:
                assert migrated.execute(f"select count(*) from {table}").fetchone()[0] == 0, table
            retired = {row[0] for row in migrated.execute("select name from sqlite_master where type='table'")} & {"branches", "history_nodes", "history_edges", "line_heads", "automation_leases", "automation_gates", "dispatch_receipts"}
            assert not retired, retired
            assert migrated.execute("select value from plugin_states where key='active_chat_id'").fetchone()[0] == "chat-a"
        finally: migrated.close()
        current = digest(source)
        repeated = subprocess.run([str(binary), "--data-dir", str(data), "--yes"], text=True, capture_output=True, timeout=30)
        assert repeated.returncode != 0 and "backup already exists" in repeated.stderr and digest(source) == current
        print("PASS offline migration preserves Chat/Role/routing only, creates default Tasks and keeps an exact legacy backup")
if __name__ == "__main__": main()
