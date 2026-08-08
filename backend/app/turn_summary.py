"""Per-turn summarization and context-bridge building for Super Workspace.

When a dispatch turn completes, the visible messages, tool calls and file
changes of that turn are condensed by the configured LLM provider chain into a
compact summary stored in the ``super_workspace_turn_summaries`` table. Those
summaries later feed new-session bootstraps and role-switch context bridges so
a role can see what happened in the chat while it was not participating.
"""

from __future__ import annotations

import time
from typing import Any

from loguru import logger

from .agent_history import SuperTurnSummary, agent_history_store
from .files import read_config
from .llm_client import LLMChainError, chat_completion
from .super_workspace_memory import recall_chat_memories

# Tool-ish events are included truncated; reasoning/session plumbing is noise
# for a summary and is skipped entirely.
_TOOL_EVENT_TYPES = {"function_call", "custom_tool_call", "tool_call", "tool_result", "patch_apply_end"}
_SKIP_EVENT_TYPES = {"reasoning", "session_update", "plan_update"}

_TURN_SUMMARY_SYSTEM_PROMPT = (
    "You condense one agent work turn from a multi-role chat into a compact briefing for other agents "
    "who did not see the turn. Be factual: never invent constraints, decisions, file names or numbers "
    "that are not present in the transcript. Keep names of files, scripts, commands and important "
    "numbers verbatim. Write in the same language as the transcript (Chinese if the transcript is Chinese). "
    "Keep the whole summary under 800 characters."
)

_TURN_SUMMARY_USER_TEMPLATE = """Below is the transcript of one agent turn (user query, assistant messages, tool calls, file changes; long tool outputs are truncated).

Write a summary with exactly these four sections:
## 任务
(what the user asked for in this turn, 1-2 sentences)
## 关键动作与改动
(what the agent did: files/scripts/commands/decisions, with concrete names; bullet points)
## 结果
(outcome: what works now, verification results, artifacts)
## 未决事项
(open questions / next steps; write "无" if none)

TRANSCRIPT:
{transcript}
"""


def _truncate(value: str, budget: int) -> str:
    text = value.strip()
    if len(text) <= budget:
        return text
    return f"{text[:budget]}\n… [truncated, {len(text) - budget} chars omitted]"


def build_turn_transcript(
    user_id: str | None,
    turn_id: str,
    query_message_id: str | None,
    tool_char_budget: int,
) -> tuple[str, int, int]:
    """Render one turn as a bounded text transcript.

    Returns (transcript, source_message_count, source_char_count). Assistant
    messages are kept in full; each tool event and each file-change diff is
    truncated to ``tool_char_budget``.
    """
    events = agent_history_store.turn_transcript_events(user_id, turn_id, query_message_id)
    blocks: list[str] = []
    count = 0
    chars = 0
    for event in events:
        event_type = str(event.get("event_type") or "")
        if event_type in _SKIP_EVENT_TYPES:
            continue
        query = str(event.get("query") or "").strip()
        text = str(event.get("text") or "").strip()
        block = ""
        if query:
            block = f"### User query\n{query}"
        elif event_type in ("message:assistant", "message:user"):
            if text:
                block = f"### Assistant\n{text}"
        elif event_type in _TOOL_EVENT_TYPES:
            if text:
                block = f"### Tool event ({event_type})\n{_truncate(text, tool_char_budget)}"
        if block:
            blocks.append(block)
            count += 1
            chars += len(block)
        for change in event.get("file_changes") or []:
            path = str(change.get("path") or "").strip()
            if not path:
                continue
            diff = str(change.get("diff") or "")
            change_block = f"### File change: {path} ({change.get('change_type') or 'update'})"
            if diff.strip():
                change_block += f"\n{_truncate(diff, tool_char_budget)}"
            blocks.append(change_block)
            count += 1
            chars += len(change_block)
    return "\n\n".join(blocks), count, chars


def generate_turn_summary(
    *,
    user_id: str | None,
    turn_id: str,
    workspace_id: str | None,
    chat_id: str,
    query_message_id: str | None,
    role_id: str | None,
    role_name: str,
    provider: str,
    occurred_at: float,
) -> SuperTurnSummary | None:
    """Summarize one completed turn via the LLM provider chain and persist it.

    Returns the stored summary, or None when there is nothing worth
    summarizing (no assistant output). Raises nothing on LLM failure — a
    failed summary row is recorded instead so the failure is auditable.
    """
    config = read_config().super_workspace
    transcript, message_count, char_count = build_turn_transcript(
        user_id, turn_id, query_message_id, int(config.turn_summary_tool_char_budget)
    )
    base = SuperTurnSummary(
        turn_id=turn_id,
        workspace_id=workspace_id,
        chat_id=chat_id,
        query_message_id=query_message_id,
        role_id=role_id,
        role_name=role_name,
        provider=provider,
        source_message_count=message_count,
        source_char_count=char_count,
        occurred_at=occurred_at,
        created_at=time.time(),
    )
    if not transcript or "### Assistant" not in transcript:
        logger.debug("Turn summary skipped: no assistant content turn_id={}", turn_id)
        return None
    user_prompt = _TURN_SUMMARY_USER_TEMPLATE.format(transcript=transcript)
    logger.info(
        "Turn summary prompt turn_id={} chat_id={} source_messages={} source_chars={}\n"
        "[system]\n{}\n[user]\n{}",
        turn_id,
        chat_id,
        message_count,
        char_count,
        _TURN_SUMMARY_SYSTEM_PROMPT,
        user_prompt,
    )
    started = time.time()
    try:
        result = chat_completion(
            [
                {"role": "system", "content": _TURN_SUMMARY_SYSTEM_PROMPT},
                {"role": "user", "content": user_prompt},
            ],
            timeout=int(config.turn_summary_timeout_seconds),
        )
    except LLMChainError as exc:
        logger.warning("Turn summary LLM chain failed turn_id={} reason={}", turn_id, exc)
        base.status = "failed"
        base.error = str(exc)[:1000]
        base.latency_ms = int((time.time() - started) * 1000)
        agent_history_store.upsert_turn_summary(base, user_id)
        return base
    except Exception as exc:
        logger.warning("Turn summary unexpected failure turn_id={} reason={}", turn_id, exc)
        base.status = "failed"
        base.error = f"{exc.__class__.__name__}: {exc}"[:1000]
        base.latency_ms = int((time.time() - started) * 1000)
        agent_history_store.upsert_turn_summary(base, user_id)
        return base
    summary_text = result.content.strip()
    base.status = "completed" if summary_text else "failed"
    base.summary = summary_text
    base.error = "" if summary_text else "LLM returned empty summary"
    base.model = result.model
    base.profile_id = result.provider_id
    base.latency_ms = int(result.latency_s * 1000)
    agent_history_store.upsert_turn_summary(base, user_id)
    logger.info(
        "Turn summary stored turn_id={} chat_id={} profile={} latency_ms={} chars={}\n[summary]\n{}",
        turn_id,
        chat_id,
        result.provider_id,
        base.latency_ms,
        len(summary_text),
        summary_text,
    )
    return base


def _format_summary_time(value: float) -> str:
    return time.strftime("%m-%d %H:%M", time.localtime(value))


def build_turn_summaries_section(
    *,
    user_id: str | None,
    workspace_id: str,
    chat_id: str,
    before_time: float,
    after_time: float | None = None,
    char_budget: int = 8000,
    exclude_role_id: str | None = None,
) -> str:
    """Formatted block of recent turn summaries for prompt injection.

    ``after_time`` restricts to turns the caller has not seen (role-switch
    bridge); None includes all recent turns (new-session bootstrap).
    ``exclude_role_id`` drops the caller's own turns: a reused session has
    already seen every turn it produced itself.

    Summaries are picked newest-first until ``char_budget`` is exhausted, so a
    fixed budget covers as many turns as possible; the newest summary is
    always included (truncated when it alone exceeds the budget).
    """
    summaries = agent_history_store.list_recent_turn_summaries(
        user_id, workspace_id, chat_id, before_time, limit=50
    )
    if after_time is not None:
        summaries = [item for item in summaries if item.occurred_at > after_time]
    if exclude_role_id is not None:
        summaries = [item for item in summaries if item.role_id != exclude_role_id]
    if not summaries or char_budget <= 0:
        return ""
    picked: list[SuperTurnSummary] = []
    used = 0
    for item in reversed(summaries):  # newest first
        text_len = len(item.summary)
        if used + text_len > char_budget:
            if not picked:
                picked.append(item.model_copy(update={"summary": _truncate(item.summary, char_budget)}))
            break
        picked.append(item)
        used += text_len
    lines = ["Summaries of earlier work turns in this chat (most recent last):"]
    for item in reversed(picked):  # chronological output
        role_label = item.role_name or item.role_id or "agent"
        lines.append(f"- [{_format_summary_time(item.occurred_at)}, role \"{role_label}\"]\n{item.summary}")
    return "\n\n".join(lines)

def build_unsummarized_tail_section(
    *,
    user_id: str | None,
    workspace_id: str,
    chat_id: str,
    before_time: float,
    after_time: float | None = None,
    token_budget: int = 1500,
    before_message_id: str | None = None,
) -> str:
    """Raw visible messages not yet covered by any turn summary.

    Covers the gap between the newest completed summary and ``before_time``
    (summaries are generated asynchronously and may lag or fail). When the
    chat has no summaries at all this degrades to the plain recent-history
    tail. Returns "" when the gap is empty.
    """
    if token_budget <= 0:
        return ""
    latest_summary = agent_history_store.latest_turn_summary_time(user_id, workspace_id, chat_id, before_time)
    floor = after_time or 0.0
    if latest_summary is not None:
        floor = max(floor, latest_summary)
    elif after_time is not None:
        floor = after_time
    else:
        floor = None  # no summaries yet: fall back to the plain recent tail
    text = agent_history_store.visible_chat_history_context(
        str(user_id or ""),
        workspace_id,
        chat_id,
        before_message_id,
        token_budget,
        after_time=floor,
        before_time=before_time,
    )
    if not text:
        return ""
    if floor is None:
        return text
    return text.replace(
        "Recent visible chat history before the current message:",
        "Recent activity not yet covered by a summary (raw messages):",
        1,
    )


def build_hindsight_recall_section(
    *,
    user_id: str,
    workspace_id: str,
    chat_id: str,
    query: str,
    recent_tail: str = "",
    max_tokens: int = 800,
    occurred_at: float | None = None,
    limit: int = 8,
) -> str:
    """Formatted block of query-relevant long-term memories (best-effort)."""
    snippets = recall_chat_memories(
        user_id=user_id,
        workspace_id=workspace_id,
        chat_id=chat_id,
        query=query,
        recent_tail=recent_tail,
        max_tokens=max_tokens,
        occurred_at=occurred_at,
    )
    if not snippets:
        return ""
    lines = ["Relevant long-term memories for this request (candidate evidence, verify against recent history):"]
    for snippet in snippets[: max(1, limit)]:
        lines.append(f"- {snippet}")
    return "\n".join(lines)


def build_role_switch_bridge(
    *,
    user_id: str | None,
    workspace_id: str,
    chat_id: str,
    role_id: str,
    query_text: str,
    before_time: float,
    before_message_id: str | None = None,
) -> str:
    """Context bridge for a reused session whose role missed chat activity.

    Returns "" when the role has no unseen activity (its own last message is
    the latest activity) or the feature is disabled.
    """
    config = read_config().super_workspace
    if not config.context_bridge_enabled:
        return ""
    last_active = agent_history_store.role_last_activity_time(user_id, workspace_id, chat_id, role_id, before_time)
    after_time = last_active if last_active is not None else 0.0
    if last_active is not None and not agent_history_store.has_visible_activity_between(
        user_id, workspace_id, chat_id, after_time, before_time
    ):
        return ""
    sections: list[str] = []
    summaries_section = build_turn_summaries_section(
        user_id=user_id,
        workspace_id=workspace_id,
        chat_id=chat_id,
        before_time=before_time,
        after_time=after_time,
        char_budget=int(config.context_bridge_summary_char_budget),
        exclude_role_id=role_id,
    )
    if summaries_section:
        sections.append(summaries_section)
    tail_section = build_unsummarized_tail_section(
        user_id=user_id,
        workspace_id=workspace_id,
        chat_id=chat_id,
        before_time=before_time,
        after_time=after_time,
        token_budget=int(config.chat_history_bootstrap_tokens),
        before_message_id=before_message_id,
    )
    if tail_section:
        sections.append(tail_section)
    if config.context_bridge_hindsight_enabled:
        recall_section = build_hindsight_recall_section(
            user_id=str(user_id or ""),
            workspace_id=workspace_id,
            chat_id=chat_id,
            query=query_text,
            max_tokens=int(config.context_bridge_hindsight_max_tokens),
            occurred_at=before_time,
        )
        if recall_section:
            sections.append(recall_section)
    if not sections:
        return ""
    return (
        "While you were away, other work happened in this chat that your session did not see. "
        "Catch up from this context:\n\n" + "\n\n".join(sections)
    )


def build_new_session_context(
    *,
    user_id: str | None,
    workspace_id: str,
    chat_id: str,
    query_text: str,
    before_time: float,
    before_message_id: str | None = None,
) -> str:
    """Summary + unsummarized-gap + recall bootstrap for a newly created session."""
    config = read_config().super_workspace
    sections: list[str] = []
    if config.context_bridge_enabled:
        summaries_section = build_turn_summaries_section(
            user_id=user_id,
            workspace_id=workspace_id,
            chat_id=chat_id,
            before_time=before_time,
            after_time=None,
            char_budget=int(config.context_bridge_summary_char_budget),
        )
        if summaries_section:
            sections.append(summaries_section)
        tail_section = build_unsummarized_tail_section(
            user_id=user_id,
            workspace_id=workspace_id,
            chat_id=chat_id,
            before_time=before_time,
            after_time=None,
            token_budget=int(config.chat_history_bootstrap_tokens),
            before_message_id=before_message_id,
        )
        if tail_section:
            sections.append(tail_section)
        if config.context_bridge_hindsight_enabled:
            recall_section = build_hindsight_recall_section(
                user_id=str(user_id or ""),
                workspace_id=workspace_id,
                chat_id=chat_id,
                query=query_text,
                recent_tail=tail_section,
                max_tokens=int(config.context_bridge_hindsight_max_tokens),
                occurred_at=before_time,
            )
            if recall_section:
                sections.append(recall_section)
    return "\n\n".join(sections)
