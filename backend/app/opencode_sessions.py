from __future__ import annotations

from .acp_sessions import ACPSession, ACPSessionManager
from .opencode_acp import OpenCodeACPRuntime
from .storage import OPENCODE_LOG_DIR


OpenCodeSession = ACPSession


class OpenCodeSessionManager(ACPSessionManager):
    """Registers OpenCode process startup with the provider-neutral ACP manager."""

    def __init__(self) -> None:
        runtime = OpenCodeACPRuntime(self._handle_acp_update)
        super().__init__(
            provider="opencode",
            acp=runtime,
            metadata_dir=OPENCODE_LOG_DIR,
        )


opencode_session_manager = OpenCodeSessionManager()
