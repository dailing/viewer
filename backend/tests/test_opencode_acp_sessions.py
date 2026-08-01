from __future__ import annotations

import unittest
from unittest.mock import AsyncMock, patch

from backend.app.opencode_acp import OpenCodeACPRuntime
from backend.app.opencode_sessions import OpenCodeSessionManager


class OpenCodeACPRegistrationTests(unittest.TestCase):
    def test_runtime_uses_opencode_acp_command(self) -> None:
        with patch.dict("os.environ", {}, clear=False):
            runtime = OpenCodeACPRuntime(AsyncMock())

        self.assertEqual(runtime.provider, "opencode")
        self.assertEqual(runtime.command, "opencode")
        self.assertEqual(runtime._agent_arguments(), ["acp"])
        self.assertTrue(runtime.enabled)
        self.assertFalse(runtime.yolo)

    def test_command_and_enabled_state_are_configurable(self) -> None:
        with patch.dict(
            "os.environ",
            {"VIEWER_OPENCODE_COMMAND": "custom-opencode", "VIEWER_OPENCODE_ACP_ENABLED": "false"},
        ):
            runtime = OpenCodeACPRuntime(AsyncMock())

        self.assertEqual(runtime.command, "custom-opencode")
        self.assertFalse(runtime.enabled)

    def test_session_manager_has_separate_provider_and_metadata(self) -> None:
        manager = OpenCodeSessionManager()

        self.assertEqual(manager.provider, "opencode")
        self.assertEqual(manager.acp.provider, "opencode")
        self.assertEqual(manager.metadata_dir.name, "opencode-sessions")


if __name__ == "__main__":
    unittest.main()
