import asyncio
from pathlib import Path

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


async def watch_root(stop_event: asyncio.Event) -> None:
    root = settings.root_resolved
    async for changes in awatch(root, stop_event=stop_event, debounce=settings.poll_delay_ms):
        for change, raw_path in changes:
            path = Path(raw_path)
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

