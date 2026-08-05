from __future__ import annotations

import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

from backend.app import restart
from scripts.supervise_viewer import ViewerSupervisor


class RestartSupervisorTests(unittest.TestCase):
    def test_restart_api_targets_supervisor_without_systemctl(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            log_path = Path(directory) / "viewer.log"
            with (
                patch.dict(os.environ, {"VIEWER_SUPERVISOR_PID": "4242", "VIEWER_LOG_FILE": str(log_path)}),
                patch("backend.app.restart.subprocess.Popen") as popen,
            ):
                result = restart.request_restart()

        command = popen.call_args.args[0]
        self.assertEqual(result["mode"], "supervisor")
        self.assertEqual(result["supervisor_pid"], 4242)
        self.assertIn("signal", command)
        self.assertIn("HUP", command)
        self.assertNotIn("systemctl", command)

    def test_supervisor_exports_stable_pid_and_exact_child_command(self) -> None:
        child = MagicMock(pid=7331)
        with patch("scripts.supervise_viewer.subprocess.Popen", return_value=child) as popen:
            supervisor = ViewerSupervisor(["python", "run.py", "--port", "18989"], Path("/workspace"), 30.0)
            supervisor.start_child()

        self.assertEqual(popen.call_args.args[0], ["python", "run.py", "--port", "18989"])
        self.assertEqual(popen.call_args.kwargs["cwd"], Path("/workspace"))
        self.assertEqual(popen.call_args.kwargs["env"]["VIEWER_SUPERVISOR_PID"], str(os.getpid()))

    def test_systemd_backend_without_supervisor_refuses_unsafe_restart(self) -> None:
        with patch.dict(os.environ, {"INVOCATION_ID": "unit-generation"}, clear=True):
            with self.assertRaisesRegex(RuntimeError, "without the supervisor"):
                restart.request_restart()

    def test_supervisor_stops_only_backend_pid_for_generation_restart(self) -> None:
        child = MagicMock(pid=7331)
        child.poll.return_value = None
        supervisor = ViewerSupervisor(["python", "run.py"], Path("/workspace"), 30.0)
        supervisor.child = child

        supervisor.stop_child()

        child.send_signal.assert_called_once()
        child.wait.assert_called_once_with(timeout=30.0)


if __name__ == "__main__":
    unittest.main()
