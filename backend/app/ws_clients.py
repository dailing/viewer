import asyncio
from dataclasses import dataclass, field

from fastapi import WebSocket

CLIENT_QUEUE_SIZE = 200
CLIENT_SEND_TIMEOUT = 1.0


@dataclass
class WebSocketClient:
    websocket: WebSocket
    queue: asyncio.Queue[dict | None] = field(default_factory=lambda: asyncio.Queue(maxsize=CLIENT_QUEUE_SIZE))
    writer_task: asyncio.Task | None = None


def add_client(clients: dict[WebSocket, WebSocketClient], websocket: WebSocket) -> WebSocketClient:
    client = WebSocketClient(websocket=websocket)
    clients[websocket] = client
    client.writer_task = asyncio.create_task(client_writer(client))
    return client


def enqueue(client: WebSocketClient, message: dict) -> bool:
    try:
        client.queue.put_nowait(message)
    except asyncio.QueueFull:
        return False
    return True


async def client_writer(client: WebSocketClient) -> None:
    try:
        while True:
            message = await client.queue.get()
            if message is None:
                return
            await asyncio.wait_for(client.websocket.send_json(message), timeout=CLIENT_SEND_TIMEOUT)
    except Exception:
        return


async def remove_client(clients: dict[WebSocket, WebSocketClient], client: WebSocketClient) -> bool:
    if clients.pop(client.websocket, None) is None:
        return False
    if client.writer_task:
        client.writer_task.cancel()
    try:
        await client.websocket.close()
    except Exception:
        pass
    return True


async def broadcast(clients: dict[WebSocket, WebSocketClient], message: dict) -> list[WebSocketClient]:
    stale: list[WebSocketClient] = []
    for client in list(clients.values()):
        if client.writer_task and client.writer_task.done():
            stale.append(client)
            continue
        if not enqueue(client, message):
            stale.append(client)
    for client in stale:
        await remove_client(clients, client)
    return stale
