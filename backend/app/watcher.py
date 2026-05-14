import asyncio
from pathlib import Path

from loguru import logger
from watchfiles import Change, awatch

from .config import settings
from .events import hub
from .models import WatchEvent
from .users import list_user_profiles, user_home_path


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


def watch_roots() -> list[Path]:
    roots = [settings.root_resolved]
    for profile in list_user_profiles():
        roots.append(user_home_path(profile.id))
    unique = {root.resolve(): root.resolve() for root in roots}
    return sorted(unique.values(), key=lambda item: len(item.parts), reverse=True)


def relative_for_roots(path: Path, roots: list[Path]) -> str:
    resolved = path.resolve(strict=False)
    for root in roots:
        try:
            return resolved.relative_to(root).as_posix()
        except ValueError:
            continue
    return resolved.as_posix()


async def watch_root(stop_event: asyncio.Event) -> None:
    roots = watch_roots()
    logger.info("Watching {}", ", ".join(root.as_posix() for root in roots))
    async for changes in awatch(
        *roots,
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
                    path=relative_for_roots(path, roots),
                    is_dir=path.is_dir() if exists else False,
                    mtime=mtime,
                )
            )
            logger.debug("File change type={} path={}", event_type(change), relative_for_roots(path, roots))
