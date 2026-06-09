from pathlib import Path
import os
import shutil

from loguru import logger

from .config import settings

VIEW_HOME = Path(os.environ.get("VIEWER_HOME", Path.home() / ".view")).expanduser()
USERS_DIR = VIEW_HOME / "users"
CONFIG_PATH = VIEW_HOME / "config.json"
WORKSPACES_PATH = VIEW_HOME / "workspaces.json"
LOOPS_DIR = VIEW_HOME / "loops"
LOOP_STATE_PATH = VIEW_HOME / "agent-loops.json"
AGENT_TASKS_DB_PATH = VIEW_HOME / "agent-tasks.sqlite3"
AGENT_HISTORY_DB_PATH = VIEW_HOME / "agent-history.sqlite3"
LOG_DIR = VIEW_HOME / "logs"
CODEX_LOG_DIR = LOG_DIR / "codex-sessions"
HERMES_LOG_DIR = LOG_DIR / "hermes-sessions"
TERMINAL_LOG_DIR = LOG_DIR / "terminals"
AGENT_LOOP_LOG_DIR = LOG_DIR / "agent-loops"
AGENT_TASK_LOG_DIR = LOG_DIR / "agent-tasks"
CODEX_RUN_DIR = Path(os.environ.get("VIEWER_CODEX_RUN_DIR", "/tmp/viewer_run/codex"))
HERMES_RUN_DIR = Path(os.environ.get("VIEWER_HERMES_RUN_DIR", "/tmp/viewer_run/hermes"))
WEAVER_RUN_DIR = Path(os.environ.get("VIEWER_WEAVER_RUN_DIR", "/tmp/viewer_run/weaver"))

LEGACY_CONFIG_PATH = settings.root_resolved / ".viewer.config.json"
LEGACY_WORKSPACES_PATH = settings.root_resolved / ".viewer.workspaces.json"

def ensure_view_home() -> None:
    for path in (VIEW_HOME, USERS_DIR, LOOPS_DIR, LOG_DIR, CODEX_LOG_DIR, HERMES_LOG_DIR, TERMINAL_LOG_DIR, AGENT_LOOP_LOG_DIR, AGENT_TASK_LOG_DIR, CODEX_RUN_DIR, HERMES_RUN_DIR, WEAVER_RUN_DIR):
        path.mkdir(parents=True, exist_ok=True)


def copy_legacy_file(source: Path, target: Path) -> None:
    if target.exists() or not source.exists():
        return
    target.parent.mkdir(parents=True, exist_ok=True)
    try:
        shutil.copy2(source, target)
        logger.info("Copied legacy viewer state {} -> {}", source, target)
    except OSError:
        logger.warning("Failed to copy legacy viewer state {} -> {}", source, target)


def migrate_legacy_state() -> None:
    ensure_view_home()
    copy_legacy_file(LEGACY_CONFIG_PATH, CONFIG_PATH)
