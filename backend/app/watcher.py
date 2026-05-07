import asyncio
from pathlib import Path

from loguru import logger
from watchfiles import Change, awatch

from .config import settings
from .events import hub
from .files import relative_for
from .models import WatchEvent


def event_type(change: Change) -> str:
    if change == Change.added:
        return "added"
    if change == Change.deleted:
        return "deleted"
    if change == Change.modified:
        return "modified"
    return "unknown"


def is_ignored_path(path: Path) -> bool:
    return False


async def watch_root(stop_event: asyncio.Event) -> None:
    root = settings.root_resolved
    logger.info("Watching {}", root)
    async for changes in awatch(
        root,
        watch_filter=lambda change, path: not is_ignored_path(Path(path)),
        stop_event=stop_event,
        debounce=settings.poll_delay_ms,
        debug=False,
    ):
        visible_changes = [(change, Path(raw_path)) for change, raw_path in changes if not is_ignored_path(Path(raw_path))]
        if not visible_changes:
            continue
        logger.debug("Detected {} filesystem change(s)", len(visible_changes))
        for change, path in visible_changes:
            exists = path.exists()
            try:
                mtime = path.stat().st_mtime if exists else None
            except OSError:
                mtime = None
            await hub.publish(
                WatchEvent(
                    type=event_type(change),
                    path=relative_for(path),
                    is_dir=path.is_dir() if exists else False,
                    mtime=mtime,
                )
            )
            logger.debug("File change type={} path={}", event_type(change), relative_for(path))
