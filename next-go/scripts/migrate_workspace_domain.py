#!/usr/bin/env python3
"""Migrate super-workspace domain data (roles, routing policies, chats) from the
legacy Python viewer (~/.view) into the next-go chat plugin DB.

Sources:
  - roles:  <src-db>.super_workspace_roles
  - policies/default id: <src-config> super_workspace.routing_policies
  - chats + pins: <src-db>.super_workspace_chats + super_workspace_chat_pins

Targets (chat plugin DB, GORM schema):
  - roles, routing_policies, chats, plugin_states(default_routing_policy_id)

Idempotent: INSERT OR REPLACE everywhere; safe to re-run.
Legacy float epoch seconds are converted to int64 milliseconds.
Chat roots are workspace-relative in the legacy DB; they are resolved against
--root-prefix (default ~/Sync). Missing directories are kept as-is and reported.

Usage:
  python3 scripts/migrate_workspace_domain.py \
      --src-db ~/.view/agent-history.sqlite3 \
      --src-config ~/.view/config.json \
      --dst-db /tmp/viewerd-live/chat.sqlite3
"""

from __future__ import annotations

import argparse
import json
import sqlite3
import sys
from pathlib import Path


def millis(value: object, fallback: int) -> int:
    if isinstance(value, (int, float)) and value > 0:
        return int(value * 1000)
    return fallback


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--src-db", required=True)
    parser.add_argument("--src-config", required=True)
    parser.add_argument("--dst-db", required=True)
    parser.add_argument("--root-prefix", default=str(Path.home() / "Sync"))
    args = parser.parse_args()

    src = sqlite3.connect(f"file:{args.src_db}?mode=ro", uri=True)
    src.row_factory = sqlite3.Row
    config = json.loads(Path(args.src_config).read_text())
    workspace_cfg = config.get("super_workspace", {})
    now_ms = 1786672000000  # deterministic fallback for missing timestamps

    roles = src.execute(
        "SELECT id, name, description, prompt, cwd, routing_policy_id,"
        " session_policy, context_recycle_percent, context_recycle_tokens,"
        " created_at, updated_at FROM super_workspace_roles"
    ).fetchall()
    policies = workspace_cfg.get("routing_policies", [])
    default_policy_id = workspace_cfg.get("default_routing_policy_id", "")
    pinned_ids = {
        row[0] for row in src.execute("SELECT chat_id FROM super_workspace_chat_pins")
    }
    chats = src.execute(
        "SELECT id, name, type, root, common_prompt, member_role_ids_json,"
        " role_routing_policy_overrides_json, created_at, updated_at"
        " FROM super_workspace_chats"
    ).fetchall()
    src.close()

    dst = sqlite3.connect(args.dst_db)
    dst.execute("PRAGMA busy_timeout = 5000")
    missing_roots: list[str] = []

    with dst:
        for role in roles:
            dst.execute(
                "INSERT OR REPLACE INTO roles (id, name, description, prompt, cwd,"
                " routing_policy_id, session_policy, context_recycle_percent,"
                " context_recycle_tokens, created_at, updated_at)"
                " VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                (
                    role["id"],
                    role["name"],
                    role["description"] or "",
                    role["prompt"] or "",
                    role["cwd"] or "",
                    role["routing_policy_id"] or "",
                    role["session_policy"] or "reuse",
                    role["context_recycle_percent"],
                    role["context_recycle_tokens"],
                    millis(role["created_at"], now_ms),
                    millis(role["updated_at"], now_ms),
                ),
            )

        for policy in policies:
            candidates = [
                {
                    "agent": c.get("agent_id", ""),
                    "provider": c.get("provider_id", ""),
                    "model": c.get("model_id", ""),
                    "parameters": c.get("parameters") or {},
                    "enabled": bool(c.get("enabled", True)),
                }
                for c in policy.get("candidates", [])
            ]
            dst.execute(
                "INSERT OR REPLACE INTO routing_policies (id, name, candidates_json,"
                " auto_failover, max_attempts, created_at, updated_at)"
                " VALUES (?,?,?,?,?,?,?)",
                (
                    policy["id"],
                    policy.get("name", policy["id"]),
                    json.dumps(candidates, ensure_ascii=False),
                    1 if policy.get("auto_failover", True) else 0,
                    int(policy.get("max_attempts", 3)),
                    now_ms,
                    now_ms,
                ),
            )

        if default_policy_id:
            dst.execute(
                "INSERT OR REPLACE INTO plugin_states (key, value) VALUES (?,?)",
                ("default_routing_policy_id", default_policy_id),
            )

        for chat in chats:
            root_raw = (chat["root"] or "").strip()
            if root_raw in ("", "."):
                root = args.root_prefix
            else:
                root = str(Path(args.root_prefix) / root_raw)
            if not Path(root).is_dir():
                missing_roots.append(f"{chat['name']} -> {root}")
            dst.execute(
                "INSERT OR REPLACE INTO chats (id, name, type, pinned, root,"
                " common_prompt, member_role_ids, role_routing_policy_overrides,"
                " created_at, updated_at, title, provider, provider_profile,"
                " provider_session_id, cwd) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                (
                    chat["id"],
                    chat["name"],
                    chat["type"] or "group",
                    1 if chat["id"] in pinned_ids else 0,
                    root,
                    chat["common_prompt"] or "",
                    chat["member_role_ids_json"] or "[]",
                    chat["role_routing_policy_overrides_json"] or "{}",
                    millis(chat["created_at"], now_ms),
                    millis(chat["updated_at"], now_ms),
                    "",
                    "",
                    "",
                    "",
                    "",
                ),
            )

    counts = {
        "roles": dst.execute("SELECT COUNT(*) FROM roles").fetchone()[0],
        "routing_policies": dst.execute(
            "SELECT COUNT(*) FROM routing_policies"
        ).fetchone()[0],
        "chats": dst.execute("SELECT COUNT(*) FROM chats").fetchone()[0],
        "pinned_chats": dst.execute(
            "SELECT COUNT(*) FROM chats WHERE pinned = 1"
        ).fetchone()[0],
    }
    default_row = dst.execute(
        "SELECT value FROM plugin_states WHERE key = 'default_routing_policy_id'"
    ).fetchone()
    dst.close()

    print(f"migrated -> {args.dst_db}")
    print(json.dumps(counts, ensure_ascii=False))
    print(f"default_routing_policy_id: {default_row[0] if default_row else '(unset)'}")
    if missing_roots:
        print("WARNING missing chat roots (kept as resolved path):")
        for item in missing_roots:
            print(f"  - {item}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
