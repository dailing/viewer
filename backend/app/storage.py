from pathlib import Path
import os

VIEW_HOME = Path(os.environ.get("VIEWER_HOME", Path.home() / ".view")).expanduser()
CONFIG_PATH = VIEW_HOME / "config.json"
AGENT_HISTORY_DB_PATH = VIEW_HOME / "agent-history.sqlite3"
LOG_DIR = VIEW_HOME / "logs"
CODEX_LOG_DIR = LOG_DIR / "codex-sessions"
HERMES_LOG_DIR = LOG_DIR / "hermes-sessions"
TERMINAL_LOG_DIR = LOG_DIR / "terminals"
CODEX_RUN_DIR = Path(os.environ.get("VIEWER_CODEX_RUN_DIR", "/tmp/viewer_run/codex"))
HERMES_RUN_DIR = Path(os.environ.get("VIEWER_HERMES_RUN_DIR", "/tmp/viewer_run/hermes"))
WEAVER_RUN_DIR = Path(os.environ.get("VIEWER_WEAVER_RUN_DIR", "/tmp/viewer_run/weaver"))

def ensure_view_home() -> None:
    for path in (VIEW_HOME, LOG_DIR, CODEX_LOG_DIR, HERMES_LOG_DIR, TERMINAL_LOG_DIR, CODEX_RUN_DIR, HERMES_RUN_DIR, WEAVER_RUN_DIR):
        path.mkdir(parents=True, exist_ok=True)
