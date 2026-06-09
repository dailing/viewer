import logging
import os
import sys
from pathlib import Path

from loguru import logger


DEFAULT_LOG_DIR = Path(os.environ.get("VIEWER_HOME", Path.home() / ".view")).expanduser() / "logs"
_configured = False


class InterceptHandler(logging.Handler):
    def emit(self, record: logging.LogRecord) -> None:
        try:
            level: str | int = logger.level(record.levelname).name
        except ValueError:
            level = record.levelno

        frame, depth = logging.currentframe(), 2
        while frame and frame.f_code.co_filename == logging.__file__:
            frame = frame.f_back
            depth += 1

        logger.opt(depth=depth, exception=record.exc_info).log(level, record.getMessage())


def current_log_path() -> Path | None:
    raw = os.environ.get("VIEWER_LOG_FILE")
    return Path(raw).expanduser().resolve() if raw else None


def configure_logging(log_file: str | Path | None = None, debug: bool | None = None) -> Path:
    global _configured

    level = "DEBUG" if (debug if debug is not None else os.environ.get("VIEWER_DEBUG") == "1") else "INFO"
    path = Path(log_file or current_log_path() or DEFAULT_LOG_DIR / "viewer.log").expanduser().resolve()
    path.parent.mkdir(parents=True, exist_ok=True)
    os.environ["VIEWER_LOG_FILE"] = path.as_posix()

    logger.remove()
    logger.add(
        sys.stderr,
        level=level,
        colorize=True,
        backtrace=level == "DEBUG",
        diagnose=level == "DEBUG",
        format="<green>{time:HH:mm:ss.SSS}</green> | <level>{level: <8}</level> | <cyan>{name}</cyan>:<cyan>{line}</cyan> - <level>{message}</level>",
    )
    logger.add(
        path,
        level="DEBUG",
        rotation="25 MB",
        retention="14 days",
        enqueue=True,
        backtrace=True,
        diagnose=level == "DEBUG",
        format="{time:YYYY-MM-DD HH:mm:ss.SSS} | {level: <8} | {process} | {name}:{function}:{line} - {message}",
    )

    logging.basicConfig(handlers=[InterceptHandler()], level=0, force=True)
    for name in ("uvicorn", "uvicorn.error", "fastapi"):
        logging.getLogger(name).handlers = [InterceptHandler()]
        logging.getLogger(name).propagate = False
    access_logger = logging.getLogger("uvicorn.access")
    access_logger.handlers = []
    access_logger.propagate = False
    access_logger.disabled = True
    logging.getLogger("watchfiles").setLevel(logging.INFO)

    _configured = True
    logger.info("Logging initialized at {}", path)
    return path


def ensure_logging() -> Path:
    if _configured:
        path = current_log_path()
        if path:
            return path
    return configure_logging()
