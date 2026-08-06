#!/usr/bin/env python3
"""Preview a query-aware new-session bootstrap for one Super Workspace message.

This is intentionally a read-only experiment. It reads the canonical visible
timeline from Viewer SQLite, asks the configured Hindsight chat bank for recall
and reflection, and writes the composed prompt to a Markdown file. It does not
change Viewer configuration, messages, sessions, or Hindsight memories.
"""

from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

from sqlalchemy import select


PROJECT_ROOT = Path(__file__).resolve().parents[1]
if PROJECT_ROOT.as_posix() not in sys.path:
    sys.path.insert(0, PROJECT_ROOT.as_posix())

from backend.app.agent_history import (  # noqa: E402
    SuperWorkspaceMessageRow,
    agent_history_store,
    rough_token_count,
    trim_to_rough_tokens,
)
from backend.app.files import read_config  # noqa: E402
from backend.app.identity import normalize_user_id  # noqa: E402
from backend.app.super_workspace_memory import (  # noqa: E402
    _api_token,
    _api_url,
    chat_memory_bank_id,
)


DEFAULT_OUTPUT = PROJECT_ROOT / "tmp_out.md"
DEFAULT_TOTAL_TOKENS = 5_000
DEFAULT_BRIEFING_TOKENS = 2_200
DEFAULT_RECALL_TOKENS = 1_400
DEFAULT_RECENT_TOKENS = 1_400


def request_json(method: str, url: str, body: dict[str, Any], timeout: float) -> dict[str, Any]:
    headers = {
        "Content-Type": "application/json",
        "User-Agent": "viewer-memory-bootstrap-preview/1",
    }
    token = _api_token()
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = urllib.request.Request(
        url,
        data=json.dumps(body, ensure_ascii=False).encode("utf-8"),
        headers=headers,
        method=method,
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = response.read().decode("utf-8")
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"Hindsight HTTP {exc.code} for {url}: {detail[:2_000]}") from exc
    parsed = json.loads(payload) if payload else {}
    return parsed if isinstance(parsed, dict) else {}


def message_row(message_id: str, user_id: str) -> SuperWorkspaceMessageRow:
    with agent_history_store.read_session() as db:
        row = db.scalar(
            select(SuperWorkspaceMessageRow).where(
                SuperWorkspaceMessageRow.id == message_id,
                SuperWorkspaceMessageRow.user_id == normalize_user_id(user_id),
            )
        )
    if row is None:
        raise ValueError(f"Unknown Super Workspace message: {message_id}")
    if not isinstance(row.query, str) or not row.query.strip():
        raise ValueError(f"Message is not a query: {message_id}")
    if not row.workspace_id or not row.conversation_id:
        raise ValueError(f"Message has no workspace/chat lineage: {message_id}")
    return row


def recall_memories(
    api_url: str,
    bank_id: str,
    query: str,
    recent_context: str,
    occurred_at: float,
    max_tokens: int,
    timeout: float,
) -> dict[str, Any]:
    url = f"{api_url}/v1/default/banks/{urllib.parse.quote(bank_id, safe='')}/memories/recall"
    # Hindsight 0.8.x caps recall queries at 500 model tokens. The tail is
    # enough to resolve deictic requests while leaving room for the request.
    retrieval_tail = recent_context[-450:]
    retrieval_query = (
        "Find earlier chat facts that are useful for the current request. The immediate timeline is included to "
        "resolve references such as 'this plan', 'continue', or 'that change'.\n\n"
        f"CURRENT REQUEST:\n{query}\n\n"
        f"IMMEDIATE TIMELINE TAIL:\n{retrieval_tail}"
    )
    return request_json(
        "POST",
        url,
        {
            "query": retrieval_query,
            "types": ["world", "experience", "observation"],
            "budget": "mid",
            "max_tokens": max_tokens,
            "query_timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(occurred_at)),
            "include": {"chunks": {}},
            "trace": True,
        },
        timeout,
    )


def reflect_briefing(
    api_url: str,
    bank_id: str,
    query: str,
    recent_context: str,
    recalled_context: str,
    max_tokens: int,
    timeout: float,
) -> dict[str, Any]:
    url = f"{api_url}/v1/default/banks/{urllib.parse.quote(bank_id, safe='')}/reflect"
    reflection_query = (
        "Create a factual handoff briefing for a brand-new AI agent session in this collaborative chat. "
        "The memory bank contains messages from the user and multiple roles/providers. Synthesize the whole chat, "
        "but prioritize context relevant to the current user query below. Do not answer the query and do not invent "
        "facts. Preserve concrete decisions, constraints, file or system state, unfinished work, and which role made "
        "an important contribution. Keep timeline entries chronological and concise.\n\n"
        f"CURRENT USER QUERY:\n{query}\n\n"
        f"CANONICAL IMMEDIATE TIMELINE FROM VIEWER SQLITE:\n{recent_context}\n\n"
        f"QUERY-AWARE HINDSIGHT RECALL CANDIDATES:\n{recalled_context}"
    )
    response_schema = {
        "type": "object",
        "properties": {
            "chat_summary": {"type": "string"},
            "goals": {"type": "array", "items": {"type": "string"}},
            "decisions": {"type": "array", "items": {"type": "string"}},
            "constraints": {"type": "array", "items": {"type": "string"}},
            "current_state": {"type": "array", "items": {"type": "string"}},
            "artifacts": {"type": "array", "items": {"type": "string"}},
            "open_work": {"type": "array", "items": {"type": "string"}},
            "role_contributions": {"type": "array", "items": {"type": "string"}},
            "query_relevant_context": {"type": "array", "items": {"type": "string"}},
            "timeline": {"type": "array", "items": {"type": "string"}},
        },
        "required": [
            "chat_summary",
            "goals",
            "decisions",
            "constraints",
            "current_state",
            "artifacts",
            "open_work",
            "role_contributions",
            "query_relevant_context",
            "timeline",
        ],
        "additionalProperties": False,
    }
    return request_json(
        "POST",
        url,
        {
            "query": reflection_query,
            "budget": "low",
            "max_tokens": max_tokens,
            "response_schema": response_schema,
            "fact_types": ["world", "experience", "observation"],
            "include": {"facts": {}},
        },
        timeout,
    )


def markdown_list(values: Any) -> str:
    if not isinstance(values, list):
        return "- (none recalled)"
    items = [str(value).strip() for value in values if str(value).strip()]
    return "\n".join(f"- {value}" for value in items) if items else "- (none recalled)"


def format_briefing(response: dict[str, Any]) -> str:
    structured = response.get("structured_output")
    if not isinstance(structured, dict):
        text = str(response.get("text") or "").strip()
        return text or "(Hindsight returned no briefing.)"
    sections = [
        ("Summary", str(structured.get("chat_summary") or "").strip() or "(none)"),
        ("Goals", markdown_list(structured.get("goals"))),
        ("Decisions", markdown_list(structured.get("decisions"))),
        ("Constraints", markdown_list(structured.get("constraints"))),
        ("Current state", markdown_list(structured.get("current_state"))),
        ("Artifacts", markdown_list(structured.get("artifacts"))),
        ("Open work", markdown_list(structured.get("open_work"))),
        ("Role contributions", markdown_list(structured.get("role_contributions"))),
        ("Query-relevant context", markdown_list(structured.get("query_relevant_context"))),
        ("Timeline", markdown_list(structured.get("timeline"))),
    ]
    return "\n\n".join(f"### {heading}\n\n{content}" for heading, content in sections)


def format_recall(response: dict[str, Any]) -> str:
    results = response.get("results")
    if not isinstance(results, list) or not results:
        return "- (no memories recalled)"
    lines: list[str] = []
    for item in results:
        if not isinstance(item, dict):
            continue
        text = str(item.get("text") or "").strip()
        if not text:
            continue
        occurred = str(item.get("occurred_start") or item.get("occurred_end") or "unknown time")
        fact_type = str(item.get("type") or "memory")
        memory_id = str(item.get("id") or "")
        lines.append(f"- [{occurred}; {fact_type}; memory {memory_id}] {text}")
    return "\n".join(lines) if lines else "- (no memories recalled)"


def recall_evidence_for_reflection(response: dict[str, Any], token_budget: int = 1_200) -> str:
    results = response.get("results")
    if not isinstance(results, list):
        return "(none)"
    evidence = "\n".join(
        f"- {str(item.get('text') or '').strip()}"
        for item in results
        if isinstance(item, dict) and str(item.get("text") or "").strip()
    )
    return trim_to_rough_tokens(evidence, token_budget).strip() or "(none)"


def bounded_section(value: str, token_budget: int) -> str:
    return trim_to_rough_tokens(value.strip(), max(0, token_budget)).strip()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--message-id", required=True, help="Current Super Workspace query message id")
    parser.add_argument("--user-id", default="dailing")
    parser.add_argument("--output", type=Path, default=DEFAULT_OUTPUT)
    parser.add_argument("--total-tokens", type=int, default=DEFAULT_TOTAL_TOKENS)
    parser.add_argument("--briefing-tokens", type=int, default=DEFAULT_BRIEFING_TOKENS)
    parser.add_argument("--recall-tokens", type=int, default=DEFAULT_RECALL_TOKENS)
    parser.add_argument("--recent-tokens", type=int, default=DEFAULT_RECENT_TOKENS)
    parser.add_argument("--timeout", type=float, default=90.0)
    args = parser.parse_args()

    row = message_row(args.message_id, args.user_id)
    query = str(row.query).strip()
    config = read_config().super_workspace
    api_url = _api_url(config.hindsight_api_url)
    if not api_url:
        raise RuntimeError("Hindsight API URL is not configured")
    bank_id = chat_memory_bank_id(
        config.hindsight_bank_prefix,
        normalize_user_id(args.user_id),
        str(row.workspace_id),
        str(row.conversation_id),
    )

    recent = agent_history_store.visible_chat_history_context(
        args.user_id,
        str(row.workspace_id),
        str(row.conversation_id),
        str(row.id),
        args.recent_tokens,
    )
    recall = recall_memories(
        api_url,
        bank_id,
        query,
        recent,
        float(row.occurred_at),
        args.recall_tokens,
        args.timeout,
    )
    reflection = reflect_briefing(
        api_url,
        bank_id,
        query,
        recent,
        recall_evidence_for_reflection(recall),
        args.briefing_tokens,
        args.timeout,
    )

    briefing_text = bounded_section(format_briefing(reflection), args.briefing_tokens)
    recall_text = bounded_section(format_recall(recall), args.recall_tokens)
    recent_text = bounded_section(recent, args.recent_tokens)
    context = (
        "# Preview: query-aware Super Workspace bootstrap\n\n"
        "> Generated from the chat-scoped Hindsight bank plus the canonical Viewer SQLite timeline.\n"
        "> This is a preview; it has not been injected into any Agent session.\n\n"
        f"- Workspace: `{row.workspace_id}`\n"
        f"- Chat: `{row.conversation_id}`\n"
        f"- Current message: `{row.id}`\n"
        f"- Memory bank: `{bank_id}`\n\n"
        "## Chat briefing\n\n"
        f"{briefing_text}\n\n"
        "## Query-relevant recalled memories\n\n"
        f"{recall_text}\n\n"
        "## Recent exact visible timeline\n\n"
        f"{recent_text or '(no earlier visible messages)'}\n\n"
        "## Current routed message\n\n"
        f"{query}\n"
    )
    context = trim_to_rough_tokens(context, max(0, args.total_tokens))
    output = args.output.expanduser().resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(context.rstrip() + "\n", encoding="utf-8")

    trace = recall.get("trace") if isinstance(recall.get("trace"), dict) else {}
    print(
        json.dumps(
            {
                "output": output.as_posix(),
                "rough_tokens": rough_token_count(context),
                "recall_results": len(recall.get("results") or []),
                "recall_seconds": trace.get("time_seconds"),
                "reflect_usage": reflection.get("usage"),
                "bank_id": bank_id,
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, RuntimeError, urllib.error.URLError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
