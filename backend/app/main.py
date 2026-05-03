import asyncio
import os
from pathlib import Path

from fastapi import FastAPI, HTTPException, Query, Request, WebSocket
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import FileResponse, PlainTextResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from loguru import logger

from .config import settings
from .codex_sessions import codex_session_manager
from .events import hub
from .files import (
    content_hash,
    get_meta,
    guess_mime,
    list_directory,
    normalize_relative,
    read_config,
    read_text,
    read_workspaces,
    resolve_path,
    set_active_workspace,
    write_config,
    write_workspace,
)
from .logging import current_log_path, ensure_logging
from .models import ClientLog, CodexCliStatus, CodexModelOptions, CodexQueueMessage, CodexSessionCreate, CodexSessionMessage, ConfigData, TerminalCreate, WorkspaceSnapshot
from .restart import request_restart, request_stop
from .terminals import terminal_manager
from .voice import connect_voice
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
    await codex_session_manager.resume_pending_queues()


@app.on_event("shutdown")
async def shutdown() -> None:
    logger.info("Shutting down viewer")
    if watch_stop_event:
        watch_stop_event.set()
    if watch_task:
        watch_task.cancel()
    await terminal_manager.shutdown()
    await codex_session_manager.shutdown()


@app.get("/api/health")
async def health() -> dict[str, str | int]:
    return {"status": "ok", "root": settings.root_resolved.as_posix(), "pid": os.getpid()}


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


@app.post("/api/admin/restart", status_code=202)
async def restart_server():
    try:
        return request_restart()
    except RuntimeError as exc:
        raise HTTPException(status_code=500, detail=str(exc)) from exc


@app.post("/api/admin/stop", status_code=202)
async def stop_server():
    try:
        return request_stop()
    except RuntimeError as exc:
        raise HTTPException(status_code=500, detail=str(exc)) from exc


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
async def file_raw(path: str, h: str | None = None):
    target = resolve_path(path)
    if not target.exists():
        raise HTTPException(status_code=404, detail="File not found")
    if not target.is_file():
        raise HTTPException(status_code=400, detail="Path is not a file")

    mime = guess_mime(target)
    etag = h or content_hash(target)
    response = FileResponse(target, media_type=mime, filename=target.name)
    response.headers["ETag"] = f"\"{etag}\""
    response.headers["Cache-Control"] = "public, max-age=31536000, immutable"
    return response


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
    current_path = normalize_relative(config.current_path)
    visit_times = {}
    for path, visited_at in config.visit_times.items():
        cleaned = normalize_relative(path)
        visit_times[cleaned] = visited_at
    return write_config(
        ConfigData(
            pinned=normalized,
            current_path=current_path,
            visit_times=visit_times,
            appearance=config.appearance,
            markdown=config.markdown,
            codex=config.codex,
            workspaces=config.workspaces,
        )
    )


@app.get("/api/workspaces")
async def get_workspaces():
    return read_workspaces()


@app.put("/api/workspaces/{workspace_id}")
async def put_workspace(workspace_id: str, snapshot: WorkspaceSnapshot):
    return write_workspace(workspace_id, snapshot)


@app.post("/api/workspaces/{workspace_id}/activate")
async def activate_workspace(workspace_id: str):
    return set_active_workspace(workspace_id)


@app.get("/api/events")
async def events():
    return StreamingResponse(hub.subscribe(), media_type="text/event-stream")


@app.get("/api/terminals")
async def terminals():
    return terminal_manager.list()


@app.post("/api/terminals")
async def create_terminal(config: TerminalCreate | None = None):
    cwd = config.cwd if config else None
    logger.info("Creating terminal cwd={}", cwd or "")
    return await terminal_manager.create(cwd)


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


@app.get("/api/codex/sessions")
async def codex_sessions():
    return codex_session_manager.list()


@app.get("/api/codex/status", response_model=CodexCliStatus)
async def codex_status():
    return codex_session_manager.cli_status()


@app.get("/api/codex/models", response_model=CodexModelOptions)
async def codex_models():
    return await codex_session_manager.model_options()


@app.post("/api/codex/sessions")
async def create_codex_session(config: CodexSessionCreate):
    logger.info("Creating Codex session cwd={}", config.cwd or "")
    return await codex_session_manager.create(config.prompt, config.cwd, config.model)


@app.get("/api/codex/sessions/{session_id}")
async def codex_session(session_id: str):
    return codex_session_manager.snapshot(session_id)


@app.post("/api/codex/sessions/{session_id}/messages")
async def send_codex_message(session_id: str, message: CodexSessionMessage):
    logger.info("Sending Codex message session={}", session_id)
    return await codex_session_manager.send(session_id, message.prompt, message.model)


@app.post("/api/codex/sessions/{session_id}/queue")
async def queue_codex_message(session_id: str, message: CodexQueueMessage):
    logger.info("Queueing Codex message session={}", session_id)
    return await codex_session_manager.enqueue(session_id, message.prompt, message.model)


@app.put("/api/codex/sessions/{session_id}/queue/{item_id}")
async def update_codex_queue_message(session_id: str, item_id: str, message: CodexQueueMessage):
    logger.info("Updating queued Codex message session={} item={}", session_id, item_id)
    return await codex_session_manager.update_queue_item(session_id, item_id, message.prompt, message.model)


@app.delete("/api/codex/sessions/{session_id}/queue/{item_id}")
async def delete_codex_queue_message(session_id: str, item_id: str):
    logger.info("Deleting queued Codex message session={} item={}", session_id, item_id)
    return await codex_session_manager.delete_queue_item(session_id, item_id)


@app.post("/api/codex/sessions/{session_id}/terminate")
async def terminate_codex_session(session_id: str):
    logger.info("Terminating Codex session {}", session_id)
    return await codex_session_manager.terminate(session_id)


@app.delete("/api/codex/sessions/{session_id}")
async def delete_codex_session(session_id: str):
    logger.info("Deleting Codex session {}", session_id)
    return await codex_session_manager.delete(session_id)


@app.websocket("/api/codex/sessions/{session_id}/ws")
async def codex_session_ws(websocket: WebSocket, session_id: str):
    await codex_session_manager.connect(session_id, websocket)


@app.websocket("/api/voice/ws")
async def voice_ws(websocket: WebSocket):
    await connect_voice(websocket)


dist = settings.frontend_dist_resolved
if dist and Path(dist).exists():
    app.mount("/", StaticFiles(directory=dist, html=True), name="frontend")
