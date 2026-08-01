from __future__ import annotations

import os

from .acp_runtime import ACPProcessConfig, ACPRuntime, ACPUpdateHandler


def _enabled(name: str, default: str) -> bool:
    return os.environ.get(name, default).strip().lower() not in {"0", "false", "no", "off"}


class OpenCodeACPRuntime(ACPRuntime):
    """OpenCode process configuration for the shared ACP runtime."""

    def __init__(self, update_handler: ACPUpdateHandler) -> None:
        command = os.environ.get("VIEWER_OPENCODE_COMMAND", "opencode").strip() or "opencode"
        super().__init__(
            ACPProcessConfig(
                provider="opencode",
                command=command,
                arguments=("acp",),
                enabled=_enabled("VIEWER_OPENCODE_ACP_ENABLED", "true"),
                profile="default",
                yolo=False,
            ),
            update_handler,
        )
