from __future__ import annotations

import os

from .acp_runtime import ACPProcessConfig, ACPRuntime, ACPSessionNotFound, ACPUpdateHandler


def _enabled(name: str, default: str) -> bool:
    return os.environ.get(name, default).strip().lower() not in {"0", "false", "no", "off"}


class HermesACPRuntime(ACPRuntime):
    """Hermes-specific process configuration for the shared ACP runtime."""

    def __init__(self, update_handler: ACPUpdateHandler) -> None:
        profile = os.environ.get("VIEWER_HERMES_PROFILE", "default").strip() or "default"
        command = os.environ.get("VIEWER_HERMES_COMMAND", "hermes").strip() or "hermes"
        yolo = _enabled("VIEWER_HERMES_YOLO", "true")
        arguments = ["-p", profile]
        if yolo:
            arguments.append("--yolo")
        arguments.append("acp")
        super().__init__(
            ACPProcessConfig(
                provider="hermes",
                command=command,
                arguments=tuple(arguments),
                enabled=_enabled("VIEWER_HERMES_ACP_ENABLED", "true"),
                profile=profile,
                yolo=yolo,
            ),
            update_handler,
        )

    async def set_model(self, session_id: str, model: str) -> None:
        """Use Hermes' dedicated ACP model method instead of configOptions."""
        await self.start()
        await self._require_connection().set_session_model(
            model_id=model,
            session_id=session_id,
        )


# Compatibility name for callers that previously caught the Hermes-specific type.
HermesACPSessionNotFound = ACPSessionNotFound
