from __future__ import annotations

from .acp_sessions import ACPSession, ACPSessionManager
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


hermes_session_manager = HermesSessionManager()
