import asyncio
from pathlib import Path

from fastapi import FastAPI, Query, Request, WebSocket
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import FileResponse, PlainTextResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from loguru import logger

from .config import settings
from .events import hub
from .files import get_meta, list_directory, read_config, read_text, resolve_path, write_config
from .logging import current_log_path, ensure_logging
from .models import ClientLog, ConfigData
from .terminals import terminal_manager
from .watcher import watch_root

ensure_logging()
app = FastAPI(title="Local Live File Viewer")
watch_stop_event: asyncio.Event | None = None
watch_task: asyncio.Task | None = None

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)


@app.middleware("http")
async def log_requests(request: Request, call_next):
    try:
        response = await call_next(request)
    except Exception:
        logger.exception("HTTP {} {} failed", request.method, request.url.path)
        raise
    if response.status_code >= 400:
        logger.warning("HTTP {} {} -> {}", request.method, request.url.path, response.status_code)
    elif settings.debug and request.url.path.startswith("/api/"):
        logger.debug("HTTP {} {} -> {}", request.method, request.url.path, response.status_code)
    return response


@app.on_event("startup")
async def startup() -> None:
    global watch_stop_event, watch_task
    settings.root_resolved.mkdir(parents=True, exist_ok=True)
    logger.info(
        "Starting viewer root={} frontend_dist={} debug={}",
        settings.root_resolved,
        settings.frontend_dist_resolved,
        settings.debug,
    )
    watch_stop_event = asyncio.Event()
    watch_task = asyncio.create_task(watch_root(watch_stop_event))


@app.on_event("shutdown")
async def shutdown() -> None:
    logger.info("Shutting down viewer")
    if watch_stop_event:
        watch_stop_event.set()
    if watch_task:
        watch_task.cancel()
    await terminal_manager.shutdown()


@app.get("/api/health")
async def health() -> dict[str, str]:
    return {"status": "ok", "root": settings.root_resolved.as_posix()}


@app.get("/api/debug/info")
async def debug_info() -> dict[str, str | bool | None]:
    log_path = current_log_path()
    return {
        "debug": settings.debug,
        "root": settings.root_resolved.as_posix(),
        "frontend_dist": settings.frontend_dist_resolved.as_posix() if settings.frontend_dist_resolved else None,
        "log_file": log_path.as_posix() if log_path else None,
    }


@app.get("/api/debug/log")
async def debug_log():
    log_path = current_log_path()
    if not log_path or not log_path.exists():
        return PlainTextResponse("No log file is configured.", status_code=404)
    return PlainTextResponse(log_path.read_text(encoding="utf-8", errors="replace"))


@app.post("/api/debug/client-log")
async def client_log(entry: ClientLog) -> dict[str, str]:
    log = logger.bind(source=entry.source, url=entry.url, user_agent=entry.user_agent)
    message = entry.message
    if entry.stack:
        message = f"{message}\n{entry.stack}"
    if entry.level == "debug":
        log.debug("Frontend: {}", message)
    elif entry.level == "info":
        log.info("Frontend: {}", message)
    elif entry.level == "warning":
        log.warning("Frontend: {}", message)
    else:
        log.error("Frontend: {}", message)
    return {"status": "ok"}


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
    logger.info("Creating terminal")
    return await terminal_manager.create()


@app.get("/api/terminals/{terminal_id}")
async def terminal(terminal_id: str):
    return terminal_manager.get(terminal_id).snapshot()


@app.post("/api/terminals/{terminal_id}/terminate")
async def terminate_terminal(terminal_id: str):
    logger.info("Terminating terminal {}", terminal_id)
    return await terminal_manager.terminate(terminal_id)


@app.delete("/api/terminals/{terminal_id}")
async def delete_terminal(terminal_id: str):
    logger.info("Deleting terminal {}", terminal_id)
    return await terminal_manager.delete(terminal_id)


@app.websocket("/api/terminals/{terminal_id}/ws")
async def terminal_ws(websocket: WebSocket, terminal_id: str):
    await terminal_manager.connect(terminal_id, websocket)


dist = settings.frontend_dist_resolved
if dist and Path(dist).exists():
    app.mount("/", StaticFiles(directory=dist, html=True), name="frontend")
