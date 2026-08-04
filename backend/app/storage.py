from pathlib import Path
import os

LEGACY_VIEW_HOME = Path(os.environ.get("VIEWER_HOME", Path.home() / ".view")).expanduser()
CONFIG_DIR = Path(os.environ.get("VIEWER_CONFIG_DIR", LEGACY_VIEW_HOME)).expanduser()
DATA_DIR = Path(os.environ.get("VIEWER_DATA_DIR", LEGACY_VIEW_HOME)).expanduser()
# Compatibility alias for modules/plugins that still treat Viewer state as one root.
VIEW_HOME = DATA_DIR
CONFIG_PATH = CONFIG_DIR / "config.json"
AGENT_HISTORY_DB_PATH = DATA_DIR / "agent-history.sqlite3"
LOG_DIR = DATA_DIR / "logs"
CODEX_LOG_DIR = LOG_DIR / "codex-sessions"
CODEX_APP_SERVER_LOG_DIR = LOG_DIR / "codex-app-server-sessions"
HERMES_LOG_DIR = LOG_DIR / "hermes-sessions"
OPENCODE_LOG_DIR = LOG_DIR / "opencode-sessions"
TERMINAL_LOG_DIR = LOG_DIR / "terminals"
CODEX_RUN_DIR = Path(os.environ.get("VIEWER_CODEX_RUN_DIR", "/tmp/viewer_run/codex"))
HERMES_RUN_DIR = Path(os.environ.get("VIEWER_HERMES_RUN_DIR", "/tmp/viewer_run/hermes"))
WEAVER_RUN_DIR = Path(os.environ.get("VIEWER_WEAVER_RUN_DIR", "/tmp/viewer_run/weaver"))

def ensure_view_home() -> None:
    for path in (CONFIG_DIR, DATA_DIR, LOG_DIR, CODEX_LOG_DIR, CODEX_APP_SERVER_LOG_DIR, HERMES_LOG_DIR, OPENCODE_LOG_DIR, TERMINAL_LOG_DIR, CODEX_RUN_DIR, HERMES_RUN_DIR, WEAVER_RUN_DIR):
        path.mkdir(parents=True, exist_ok=True)
