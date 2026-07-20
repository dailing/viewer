from __future__ import annotations

import re

from .acp_sessions import ACPSession, ACPSessionManager
from .models import AgentEventType
from .hermes_acp import HermesACPRuntime
from .storage import HERMES_LOG_DIR


HermesSession = ACPSession


class HermesSessionManager(ACPSessionManager):
    """Registers Hermes process startup with the provider-neutral ACP manager."""

    def __init__(self) -> None:
        runtime = HermesACPRuntime(self._handle_acp_update)
        super().__init__(
            provider="hermes",
            acp=runtime,
            metadata_dir=HERMES_LOG_DIR,
            legacy_provider_session_key="hermes_session_id",
            legacy_profile_key="hermes_profile",
        )

    def _provider_turn_error(self, session: ACPSession, event_start: int) -> str | None:
        if not session.lineage:
            return None
        assistant_messages = [
            str(event.get("text") or "").strip()
            for event in session.acp_events[event_start:]
            if event.get("event_type") == AgentEventType.MESSAGE_ASSISTANT
            and str(event.get("text") or "").strip()
        ]
        if not assistant_messages:
            return "Hermes ACP completed without an assistant response"
        final_text = assistant_messages[-1]
        if re.match(
            r"^(?:API call failed after \d+ retries|Error:|Language Model(?: Error)?:|I apologize, but I encountered repeated errors:)",
            final_text,
            flags=re.IGNORECASE,
        ):
            return final_text
        return None


hermes_session_manager = HermesSessionManager()
