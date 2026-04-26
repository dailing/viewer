import asyncio
import json
from collections.abc import AsyncIterator

from .models import WatchEvent


class EventHub:
    def __init__(self) -> None:
        self._subscribers: set[asyncio.Queue[WatchEvent]] = set()

    async def publish(self, event: WatchEvent) -> None:
        dead: list[asyncio.Queue[WatchEvent]] = []
        for queue in self._subscribers:
            try:
                queue.put_nowait(event)
            except asyncio.QueueFull:
                dead.append(queue)
        for queue in dead:
            self._subscribers.discard(queue)

    async def subscribe(self) -> AsyncIterator[str]:
        queue: asyncio.Queue[WatchEvent] = asyncio.Queue(maxsize=100)
        self._subscribers.add(queue)
        try:
            yield "event: ready\ndata: {}\n\n"
            while True:
                event = await queue.get()
                data = json.dumps(event.model_dump())
                yield f"event: file-change\ndata: {data}\n\n"
        finally:
            self._subscribers.discard(queue)


hub = EventHub()

