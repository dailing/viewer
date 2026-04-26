import asyncio
from pathlib import Path

from fastapi import FastAPI, Query, WebSocket
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import FileResponse, PlainTextResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles

from .config import settings
from .events import hub
from .files import get_meta, list_directory, read_config, read_text, resolve_path, write_config
from .models import ConfigData
from .terminals import terminal_manager
from .watcher import watch_root

app = FastAPI(title="Local Live File Viewer")
watch_stop_event: asyncio.Event | None = None
watch_task: asyncio.Task | None = None

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)


@app.on_event("startup")
async def startup() -> None:
    global watch_stop_event, watch_task
    settings.root_resolved.mkdir(parents=True, exist_ok=True)
    watch_stop_event = asyncio.Event()
    watch_task = asyncio.create_task(watch_root(watch_stop_event))


@app.on_event("shutdown")
async def shutdown() -> None:
    if watch_stop_event:
        watch_stop_event.set()
    if watch_task:
        watch_task.cancel()
    await terminal_manager.shutdown()


@app.get("/api/health")
async def health() -> dict[str, str]:
    return {"status": "ok", "root": settings.root_resolved.as_posix()}


@app.get("/api/tree")
async def tree(path: str = Query(default="")):
    return list_directory(path)


@app.get("/api/file/meta")
async def file_meta(path: str):
    return get_meta(path)


@app.get("/api/file/content", response_class=PlainTextResponse)
async def file_content(path: str):
    return read_text(path)


@app.get("/api/file/raw")
async def file_raw(path: str):
    meta = get_meta(path)
    return FileResponse(resolve_path(path), media_type=meta.mime, filename=meta.name)


@app.get("/api/config")
async def get_config():
    return read_config()


@app.put("/api/config")
async def put_config(config: ConfigData):
    normalized = []
    seen = set()
    for item in config.pinned:
        cleaned = item.replace("\\", "/").strip("/")
        if cleaned not in seen:
            normalized.append(cleaned)
            seen.add(cleaned)
    return write_config(ConfigData(pinned=normalized))


@app.get("/api/events")
async def events():
    return StreamingResponse(hub.subscribe(), media_type="text/event-stream")


@app.get("/api/terminals")
async def terminals():
    return terminal_manager.list()


@app.post("/api/terminals")
async def create_terminal():
    return await terminal_manager.create()


@app.get("/api/terminals/{terminal_id}")
async def terminal(terminal_id: str):
    return terminal_manager.get(terminal_id).snapshot()


@app.post("/api/terminals/{terminal_id}/terminate")
async def terminate_terminal(terminal_id: str):
    return await terminal_manager.terminate(terminal_id)


@app.delete("/api/terminals/{terminal_id}")
async def delete_terminal(terminal_id: str):
    return await terminal_manager.delete(terminal_id)


@app.websocket("/api/terminals/{terminal_id}/ws")
async def terminal_ws(websocket: WebSocket, terminal_id: str):
    await terminal_manager.connect(terminal_id, websocket)


dist = settings.frontend_dist_resolved
if dist and Path(dist).exists():
    app.mount("/", StaticFiles(directory=dist, html=True), name="frontend")
