import asyncio
import os
import re
import time
from pathlib import Path

from fastapi import FastAPI, HTTPException, Query, Request, WebSocket
from fastapi.middleware.cors import CORSMiddleware
from fastapi.middleware.gzip import GZipMiddleware
from fastapi.responses import FileResponse, HTMLResponse, PlainTextResponse, Response, StreamingResponse
from fastapi.staticfiles import StaticFiles
from loguru import logger

from .agent_history import SuperHistoryRunCreate, agent_history_store
from .config import settings
from .codex_sessions import codex_session_manager
from .events import hub
from .files import (
    content_hash,
    delete_file,
    entry_for,
    get_meta,
    guess_mime,
    list_directory,
    metadata_version,
    read_config,
    read_text,
    read_text_lines,
    resolve_directory_link,
    resolve_markdown_link,
    resolve_path,
    upload_target,
    write_config,
    write_text,
)
from .git_diff import git_commit, git_diff, git_push, git_revert, git_stage, git_status
from .hermes_sessions import hermes_session_manager
from .logging import ensure_logging
from .models import ConfigData, GitCommitRequest, GitRevertRequest, GitStageRequest, TerminalCreate
from .restart import request_restart, request_stop
from .super_workspace import SuperChatCreate, SuperChatPatch, SuperDispatchRequest, SuperRoleCreate, SuperRolePatch, SuperWorkspacePatch, super_workspace_manager
from .super_workspace_runtime import SuperWorkspaceMessageCreate, super_workspace_runtime
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
app.add_middleware(GZipMiddleware, minimum_size=1024)

AGENT_PROVIDERS = {
    "codex": {"id": "codex", "name": "Codex", "icon": "bi-stars"},
    "hermes": {"id": "hermes", "name": "Hermes", "icon": "bi-lightning-charge"},
}

NOISY_SUCCESS_PATHS = {
    "/api/git/status",
    "/api/super-workspace/runs",
    "/api/terminals",
}
SLOW_REQUEST_MS = 1000.0


def _is_noisy_success_path(path: str) -> bool:
    return path in NOISY_SUCCESS_PATHS or path.startswith("/assets/")


@app.middleware("http")
async def log_requests(request: Request, call_next):
    started = time.perf_counter()
    try:
        response = await call_next(request)
    except Exception:
        elapsed_ms = (time.perf_counter() - started) * 1000
        logger.exception("HTTP {} {} failed", request.method, request.url.path)
        raise
    elapsed_ms = (time.perf_counter() - started) * 1000
    if response.status_code >= 400:
        logger.warning("HTTP {} {} -> {} {:.1f}ms", request.method, request.url.path, response.status_code, elapsed_ms)
    elif elapsed_ms >= SLOW_REQUEST_MS:
        logger.info("HTTP {} {} -> {} {:.1f}ms", request.method, request.url.path, response.status_code, elapsed_ms)
    elif settings.debug and request.url.path.startswith("/api/") and not _is_noisy_success_path(request.url.path):
        logger.debug("HTTP {} {} -> {} {:.1f}ms", request.method, request.url.path, response.status_code, elapsed_ms)
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
    await super_workspace_runtime.start()


@app.on_event("shutdown")
async def shutdown() -> None:
    logger.info("Shutting down viewer")
    if watch_stop_event:
        watch_stop_event.set()
    if watch_task:
        watch_task.cancel()
    await super_workspace_runtime.shutdown()
    await terminal_manager.shutdown()
    await codex_session_manager.shutdown()
    await hermes_session_manager.shutdown()


@app.get("/api/health")
async def health() -> dict[str, str | int]:
    return {"status": "ok", "pid": os.getpid()}


@app.post("/api/admin/restart", status_code=202)
async def restart_server(include_worker: bool = False):
    try:
        return request_restart(include_worker=include_worker)
    except RuntimeError as exc:
        raise HTTPException(status_code=500, detail=str(exc)) from exc


@app.post("/api/admin/stop", status_code=202)
async def stop_server():
    try:
        return request_stop()
    except RuntimeError as exc:
        raise HTTPException(status_code=500, detail=str(exc)) from exc


@app.get("/api/tree")
async def tree(path: str = Query(default="")):
    return list_directory(path)


@app.get("/api/file/meta")
async def file_meta(path: str):
    return get_meta(path)


@app.get("/api/file/content", response_class=PlainTextResponse)
async def file_content(path: str):
    return read_text(path)


@app.get("/api/file/text-lines")
async def file_text_lines(path: str, start: int = Query(default=0, ge=0), count: int = Query(default=200, ge=1, le=500)):
    return read_text_lines(path, start, count)


@app.put("/api/file/content")
async def file_content_save(request: Request, path: str):
    content = (await request.body()).decode("utf-8")
    return write_text(path, content)


@app.post("/api/file/upload")
async def file_upload(request: Request, directory: str = Query(default=""), filename: str = Query(...)):
    target = upload_target(directory, filename)
    with target.open("wb") as handle:
        async for chunk in request.stream():
            handle.write(chunk)
    return {"status": "ok", "directory": directory, "entry": entry_for(target)}


@app.delete("/api/file")
async def file_delete(path: str):
    return delete_file(path)


@app.get("/api/file/raw")
async def file_raw(path: str, h: str | None = None, base: str | None = None):
    target_path = resolve_markdown_link(base, path) if base is not None else path
    target = resolve_path(target_path)
    if not target.exists():
        raise HTTPException(status_code=404, detail="File not found")
    if not target.is_file():
        raise HTTPException(status_code=400, detail="Path is not a file")

    mime = guess_mime(target)
    etag = h or content_hash(target)
    response = FileResponse(target, media_type=mime, filename=target.name, content_disposition_type="inline")
    response.headers["ETag"] = f"\"{etag}\""
    response.headers["Cache-Control"] = "public, max-age=31536000, immutable"
    return response


def _html_base_href(path: str) -> str:
    normalized = path.replace("\\", "/").strip("/")
    directory = normalized.rsplit("/", 1)[0] if "/" in normalized else ""
    prefix = "/api/file/site/"
    if directory:
        return f"{prefix}{'/'.join(quote_path_segment(part) for part in directory.split('/') if part)}/"
    return prefix


def quote_path_segment(value: str) -> str:
    from urllib.parse import quote

    return quote(value, safe="")


def site_url_for_root_relative(value: str) -> str:
    from urllib.parse import unquote, urlsplit

    parsed = urlsplit(value)
    path = unquote(parsed.path).strip("/")
    encoded_path = "/".join(quote_path_segment(part) for part in path.split("/") if part)
    suffix = ""
    if parsed.query:
        suffix += f"?{parsed.query}"
    if parsed.fragment:
        suffix += f"#{parsed.fragment}"
    return f"/api/file/site/{encoded_path}{suffix}"


def rewrite_html_root_relative_urls(source: str) -> str:
    def replace_attr(match: re.Match[str]) -> str:
        prefix, quote, value = match.groups()
        if not value.startswith("/") or value.startswith("//"):
            return match.group(0)
        return f"{prefix}{quote}{site_url_for_root_relative(value)}{quote}"

    rewritten = re.sub(r'(\b(?:src|href|poster|data|action)\s*=\s*)(["\'])(/[^"\']*)\2', replace_attr, source, flags=re.IGNORECASE)

    def replace_srcset(match: re.Match[str]) -> str:
        prefix, quote, value = match.groups()
        parts = []
        for candidate in value.split(","):
            stripped = candidate.strip()
            if not stripped:
                continue
            url, *descriptor = stripped.split()
            if url.startswith("/") and not url.startswith("//"):
                url = site_url_for_root_relative(url)
            parts.append(" ".join([url, *descriptor]))
        return f"{prefix}{quote}{', '.join(parts)}{quote}"

    return re.sub(r'(\bsrcset\s*=\s*)(["\'])([^"\']*)\2', replace_srcset, rewritten, flags=re.IGNORECASE)


def rewrite_css_root_relative_urls(source: str) -> str:
    def replace_url(match: re.Match[str]) -> str:
        prefix, value, suffix = match.groups()
        stripped = value.strip()
        if not stripped.startswith("/") or stripped.startswith("//"):
            return match.group(0)
        return f"{prefix}{site_url_for_root_relative(stripped)}{suffix}"

    rewritten = re.sub(r'(url\(\s*["\']?)(/[^)"\']*)(["\']?\s*\))', replace_url, source, flags=re.IGNORECASE)
    return re.sub(r'(@import\s+["\'])(/[^"\']*)(["\'])', replace_url, rewritten, flags=re.IGNORECASE)


def inject_html_base(source: str, href: str) -> str:
    base = f'<base href="{href}">'
    source = rewrite_html_root_relative_urls(source)
    if "<base" in source[:2048].lower():
        return source
    lower = source.lower()
    head_index = lower.find("<head")
    if head_index >= 0:
        head_end = source.find(">", head_index)
        if head_end >= 0:
            return f"{source[:head_end + 1]}{base}{source[head_end + 1:]}"
    return f"{base}{source}"


def resolve_site_path(path: str, user: str | None = None) -> Path:
    target = resolve_path(path, user)
    if target.exists():
        return target

    parts = [part for part in path.replace("\\", "/").strip("/").split("/") if part]
    for index in range(len(parts) - 1):
        if parts[index : index + 2] != ["generated", "assets"]:
            continue
        asset_tail = parts[index + 2 :]
        for prefix_length in range(index - 1, -1, -1):
            candidate = resolve_path("/".join([*parts[:prefix_length], "generated", "assets", *asset_tail]), user)
            if candidate.exists():
                return candidate
    return target


def file_site_response(path: str, h: str | None = None, user: str | None = None):
    target = resolve_site_path(path, user)
    if target.exists() and target.is_dir():
        index = target / "index.html"
        if index.exists() and index.is_file():
            target = index
        else:
            raise HTTPException(status_code=400, detail="Directory has no index.html")
    if not target.exists():
        raise HTTPException(status_code=404, detail="File not found")
    if not target.is_file():
        raise HTTPException(status_code=400, detail="Path is not a file")

    mime = guess_mime(target)
    if mime == "text/html" or target.suffix.lower() in {".html", ".htm"}:
        try:
            source = target.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            source = target.read_text(encoding="utf-8", errors="replace")
        relative_path = path
        response = HTMLResponse(inject_html_base(source, _html_base_href(relative_path)))
    elif mime == "text/css" or target.suffix.lower() == ".css":
        try:
            source = target.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            source = target.read_text(encoding="utf-8", errors="replace")
        response = Response(rewrite_css_root_relative_urls(source), media_type="text/css")
    else:
        response = FileResponse(target, media_type=mime, filename=target.name, content_disposition_type="inline")

    response.headers["ETag"] = f"\"{h or content_hash(target)}\""
    response.headers["Cache-Control"] = "no-cache"
    return response


@app.get("/api/file/site")
async def file_site_query(path: str, h: str | None = None):
    return file_site_response(path, h)


@app.get("/api/file/site/{path:path}")
async def file_site(path: str, h: str | None = None):
    return file_site_response(path, h)


@app.get("/api/file/resolve-link")
async def file_resolve_link(base: str, target: str):
    path = resolve_markdown_link(base, target)
    resolved = resolve_path(path)
    payload = {"path": path}
    if resolved.exists() and resolved.is_file():
        payload["content_hash"] = metadata_version(resolved, resolved.stat())
    return payload


@app.get("/api/file/resolve-directory-link")
async def file_resolve_directory_link(base: str = "", target: str = ""):
    return {"path": resolve_directory_link(base, target)}


@app.get("/api/git/status")
async def git_status_route(scope: str):
    return git_status(scope)


@app.get("/api/git/diff")
async def git_diff_route(path: str):
    return git_diff(path)


@app.post("/api/git/stage")
async def git_stage_route(request: GitStageRequest | None = None):
    return git_stage(request.path if request else None, request.scope if request else None)


@app.post("/api/git/revert")
async def git_revert_route(request: GitRevertRequest):
    return git_revert(request.path)


@app.post("/api/git/commit")
async def git_commit_route(request: GitCommitRequest):
    return git_commit(request)


@app.post("/api/git/push")
async def git_push_route(scope: str):
    return git_push(scope)


@app.get("/api/config")
async def get_config():
    return read_config()


@app.put("/api/config")
async def put_config(config: ConfigData):
    return write_config(
        ConfigData(
            appearance=config.appearance,
            markdown=config.markdown,
            codex=config.codex,
            voice=config.voice,
            super_workspace=config.super_workspace,
        )
    )


@app.get("/api/events")
async def events():
    return StreamingResponse(hub.subscribe(), media_type="text/event-stream")


@app.get("/api/terminals")
async def terminals():
    return terminal_manager.list()


@app.post("/api/terminals")
async def create_terminal(config: TerminalCreate):
    logger.info("Creating terminal cwd={}", config.cwd)
    return await terminal_manager.create(config.cwd)


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


@app.get("/api/agents/providers")
async def agent_providers():
    return list(AGENT_PROVIDERS.values())


@app.get("/api/super-workspace")
async def super_workspace():
    return super_workspace_manager.read()


@app.put("/api/super-workspace")
async def update_super_workspace(request: SuperWorkspacePatch):
    return super_workspace_manager.update(request)


@app.get("/api/super-workspace/chats")
async def super_chats():
    return super_workspace_manager.list_chats()


@app.post("/api/super-workspace/chats")
async def create_super_chat(request: SuperChatCreate):
    return super_workspace_manager.create_chat(request)


@app.put("/api/super-workspace/chats/{chat_id}")
async def update_super_chat(chat_id: str, request: SuperChatPatch):
    return super_workspace_manager.update_chat(chat_id, request)


@app.delete("/api/super-workspace/chats/{chat_id}")
async def delete_super_chat(chat_id: str):
    return super_workspace_manager.delete_chat(chat_id)


@app.post("/api/super-workspace/active-chat/{chat_id}")
async def activate_super_chat(chat_id: str):
    return super_workspace_manager.activate_chat(chat_id)


@app.post("/api/super-workspace/roles")
async def create_super_role(request: SuperRoleCreate):
    return super_workspace_manager.create_role(request)


@app.put("/api/super-workspace/roles/{role_id}")
async def update_super_role(role_id: str, request: SuperRolePatch):
    return super_workspace_manager.update_role(role_id, request)


@app.delete("/api/super-workspace/roles/{role_id}")
async def delete_super_role(role_id: str):
    return super_workspace_manager.delete_role(role_id)


@app.get("/api/super-workspace/runs")
async def super_workspace_runs(
    limit: int = Query(default=30, ge=1, le=100),
    before: float | None = None,
    after: float | None = None,
    chat_id: str | None = None,
):
    return agent_history_store.list_super_display_items(None, limit=limit, before=before, after=after, chat_id=chat_id)


@app.get("/api/super-workspace/events")
async def super_workspace_events():
    return StreamingResponse(super_workspace_runtime.event_hub.subscribe(None), media_type="text/event-stream")


@app.post("/internal/super-workspace/notify")
async def notify_super_workspace(event: dict):
    await super_workspace_runtime.notify(event)
    return {"ok": True}


@app.post("/api/super-workspace/runs")
async def create_super_workspace_run(request: SuperHistoryRunCreate):
    message = request.message.strip()
    if not message:
        raise HTTPException(status_code=400, detail="Message is required")
    return await super_workspace_runtime.submit(
        SuperWorkspaceMessageCreate(
            message=message,
            content_blocks=request.content_blocks,
            chat_id=request.chat_id,
            role_ids=request.role_ids,
            parent_message_id=request.parent_message_id,
            sender_role_id=request.sender_role_id,
        ),
        None,
    )


@app.post("/api/super-workspace/targets/{task_id}/stop")
async def stop_super_workspace_target(task_id: str):
    return await super_workspace_runtime.stop_dispatch_task(task_id)


@app.post("/api/super-workspace/dispatch")
async def dispatch_super_workspace(request: SuperDispatchRequest):
    return await super_workspace_manager.dispatch(request)


@app.websocket("/api/voice/ws")
async def voice_ws(websocket: WebSocket):
    await connect_voice(websocket)


dist = settings.frontend_dist_resolved
if dist and Path(dist).exists():
    app.mount("/", StaticFiles(directory=dist, html=True), name="frontend")
