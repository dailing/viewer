import asyncio
import os
import re
from pathlib import Path

from fastapi import FastAPI, HTTPException, Query, Request, WebSocket
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import FileResponse, HTMLResponse, PlainTextResponse, Response, StreamingResponse
from fastapi.staticfiles import StaticFiles
from loguru import logger

from .agent_loops import agent_loop_manager
from .config import settings
from .codex_sessions import codex_session_manager
from .events import hub
from .files import (
    add_workspace_agent_session,
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
    read_workspaces,
    resolve_directory_link,
    resolve_markdown_link,
    resolve_path,
    remove_workspace_agent_session,
    set_active_workspace,
    upload_target,
    write_config,
    write_text,
    write_workspace_config,
    write_workspace,
)
from .git_diff import git_commit, git_diff, git_push, git_revert, git_stage, git_status
from .hermes_sessions import hermes_session_manager
from .logging import current_log_path, ensure_logging
from .models import AgentApprovalDecision, AgentLoopCreate, AgentLoopDefinition, AgentLoopRunRequest, AgentProviderRequest, AgentQueueMessage, AgentSessionCreate, AgentSessionMessage, ClientLog, CodexCliStatus, CodexModelOptions, CodexQueueMessage, CodexSessionCreate, CodexSessionMessage, ConfigData, GitCommitRequest, GitRevertRequest, GitStageRequest, HermesQueueMessage, HermesSessionCreate, HermesSessionMessage, TerminalCreate, WorkspaceAgentSessionRequest, WorkspaceConfig, WorkspaceSnapshot
from .restart import request_restart, request_stop
from .terminals import terminal_manager
from .users import get_user_profile, list_user_profiles, user_home_path, user_home_relative
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

AGENT_PROVIDERS = {
    "codex": {"id": "codex", "name": "Codex", "icon": "bi-stars"},
    "hermes": {"id": "hermes", "name": "Hermes", "icon": "bi-lightning-charge"},
}

AGENT_MANAGERS = {
    "codex": codex_session_manager,
    "hermes": hermes_session_manager,
}


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
    for profile in list_user_profiles():
        user_home_path(profile.id).mkdir(parents=True, exist_ok=True)
    logger.info(
        "Starting viewer root={} frontend_dist={} debug={}",
        settings.root_resolved,
        settings.frontend_dist_resolved,
        settings.debug,
    )
    watch_stop_event = asyncio.Event()
    watch_task = asyncio.create_task(watch_root(watch_stop_event))
    await codex_session_manager.resume_pending_queues()
    await hermes_session_manager.resume_pending_queues()
    await agent_loop_manager.start()


@app.on_event("shutdown")
async def shutdown() -> None:
    logger.info("Shutting down viewer")
    if watch_stop_event:
        watch_stop_event.set()
    if watch_task:
        watch_task.cancel()
    await agent_loop_manager.shutdown()
    await terminal_manager.shutdown()
    await codex_session_manager.shutdown()
    await hermes_session_manager.shutdown()


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


@app.get("/api/users")
async def users():
    return [
        {**profile.model_dump(), "home_path": user_home_relative(profile.id)}
        for profile in list_user_profiles()
    ]


@app.get("/api/users/current")
async def current_user(user: str | None = None):
    profile = get_user_profile(user)
    return {**profile.model_dump(), "home_path": user_home_relative(profile.id), "cwd": user_home_path(profile.id).as_posix()}


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
async def tree(path: str = Query(default=""), user: str | None = None):
    return list_directory(path, user)


@app.get("/api/file/meta")
async def file_meta(path: str, user: str | None = None):
    return get_meta(path, user)


@app.get("/api/file/content", response_class=PlainTextResponse)
async def file_content(path: str, user: str | None = None):
    return read_text(path, user)


@app.get("/api/file/text-lines")
async def file_text_lines(path: str, start: int = Query(default=0, ge=0), count: int = Query(default=200, ge=1, le=500), user: str | None = None):
    return read_text_lines(path, start, count, user)


@app.put("/api/file/content")
async def file_content_save(request: Request, path: str, user: str | None = None):
    content = (await request.body()).decode("utf-8")
    return write_text(path, content, user)


@app.post("/api/file/upload")
async def file_upload(request: Request, directory: str = Query(default=""), filename: str = Query(...), user: str | None = None):
    target = upload_target(directory, filename, user)
    with target.open("wb") as handle:
        async for chunk in request.stream():
            handle.write(chunk)
    return {"status": "ok", "directory": directory, "entry": entry_for(target, user)}


@app.delete("/api/file")
async def file_delete(path: str, user: str | None = None):
    return delete_file(path, user)


@app.get("/api/file/raw")
async def file_raw(path: str, h: str | None = None, base: str | None = None, user: str | None = None):
    target_path = resolve_markdown_link(base, path, user) if base is not None else path
    target = resolve_path(target_path, user)
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


@app.get("/api/file/site/{path:path}")
async def file_site(path: str, h: str | None = None, user: str | None = None):
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


@app.get("/api/file/resolve-link")
async def file_resolve_link(base: str, target: str, user: str | None = None):
    path = resolve_markdown_link(base, target, user)
    resolved = resolve_path(path, user)
    payload = {"path": path}
    if resolved.exists() and resolved.is_file():
        payload["content_hash"] = metadata_version(resolved, resolved.stat())
    return payload


@app.get("/api/file/resolve-directory-link")
async def file_resolve_directory_link(base: str = "", target: str = "", user: str | None = None):
    return {"path": resolve_directory_link(base, target, user)}


@app.get("/api/git/status")
async def git_status_route(scope: str | None = None, user: str | None = None):
    return git_status(scope, user)


@app.get("/api/git/diff")
async def git_diff_route(path: str, user: str | None = None):
    return git_diff(path, user)


@app.post("/api/git/stage")
async def git_stage_route(request: GitStageRequest | None = None, user: str | None = None):
    return git_stage(request.path if request else None, request.scope if request else None, user)


@app.post("/api/git/revert")
async def git_revert_route(request: GitRevertRequest, user: str | None = None):
    return git_revert(request.path, user)


@app.post("/api/git/commit")
async def git_commit_route(request: GitCommitRequest, user: str | None = None):
    return git_commit(request, user)


@app.post("/api/git/push")
async def git_push_route(scope: str | None = None, user: str | None = None):
    return git_push(scope, user)


@app.get("/api/config")
async def get_config():
    return read_config()


@app.put("/api/config")
async def put_config(config: ConfigData):
    current = read_config()
    return write_config(
        ConfigData(
            appearance=config.appearance,
            markdown=config.markdown,
            codex=config.codex,
            workspace=config.workspace,
            users=config.users or current.users,
            default_user=config.default_user or current.default_user,
        )
    )


@app.get("/api/workspaces")
async def get_workspaces(user: str | None = None):
    return read_workspaces(user)


@app.get("/api/workspaces/config")
async def get_workspace_config():
    return read_config().workspace


@app.put("/api/workspaces/config")
async def put_workspace_config(config: WorkspaceConfig, user: str | None = None):
    return write_workspace_config(config, user)


@app.put("/api/workspaces/{workspace_id}")
async def put_workspace(workspace_id: str, snapshot: WorkspaceSnapshot, user: str | None = None):
    return write_workspace(workspace_id, snapshot, user)


@app.post("/api/workspaces/{workspace_id}/agent-sessions")
async def add_workspace_agent_session_route(workspace_id: str, request: WorkspaceAgentSessionRequest, user: str | None = None):
    return add_workspace_agent_session(workspace_id, request.ref, user)


@app.delete("/api/workspaces/{workspace_id}/agent-sessions")
async def remove_workspace_agent_session_route(workspace_id: str, request: WorkspaceAgentSessionRequest, user: str | None = None):
    return remove_workspace_agent_session(workspace_id, request.ref, user)


@app.post("/api/workspaces/{workspace_id}/activate")
async def activate_workspace(workspace_id: str, user: str | None = None):
    return set_active_workspace(workspace_id, user)


@app.get("/api/agent-loops")
async def agent_loops():
    return agent_loop_manager.list()


@app.post("/api/agent-loops")
async def create_agent_loop(config: AgentLoopCreate):
    return agent_loop_manager.create(config)


@app.post("/api/agent-loops/reload")
async def reload_agent_loops():
    return agent_loop_manager.reload()


@app.get("/api/agent-loops/{task_id}")
async def agent_loop(task_id: str):
    return agent_loop_manager.get(task_id)


@app.put("/api/agent-loops/{task_id}")
async def update_agent_loop(task_id: str, definition: AgentLoopDefinition):
    return agent_loop_manager.update(task_id, definition)


@app.delete("/api/agent-loops/{task_id}")
async def delete_agent_loop(task_id: str):
    return agent_loop_manager.delete(task_id)


@app.post("/api/agent-loops/{task_id}/run")
async def run_agent_loop(task_id: str, request: AgentLoopRunRequest | None = None):
    return await agent_loop_manager.run_now(task_id, request.trigger if request else "manual")


@app.post("/api/agent-loops/{task_id}/pause")
async def pause_agent_loop(task_id: str):
    return agent_loop_manager.pause(task_id, True)


@app.post("/api/agent-loops/{task_id}/resume")
async def resume_agent_loop(task_id: str):
    return agent_loop_manager.pause(task_id, False)


@app.post("/api/agent-loops/{task_id}/reset-session")
async def reset_agent_loop_session(task_id: str):
    return agent_loop_manager.reset_session(task_id)


@app.get("/api/agent-loops/{task_id}/runs")
async def agent_loop_runs(task_id: str):
    return agent_loop_manager.runs(task_id)


@app.get("/api/agent-loops/{task_id}/runs/{run_id}")
async def agent_loop_run_detail(task_id: str, run_id: str):
    return agent_loop_manager.run_detail(task_id, run_id)


@app.get("/api/events")
async def events():
    return StreamingResponse(hub.subscribe(), media_type="text/event-stream")


@app.get("/api/terminals")
async def terminals(user: str | None = None):
    return terminal_manager.list(user)


@app.post("/api/terminals")
async def create_terminal(config: TerminalCreate | None = None, user: str | None = None):
    cwd = config.cwd if config else None
    logger.info("Creating terminal cwd={}", cwd or "")
    return await terminal_manager.create(cwd, user)


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


def _agent_manager(provider: str):
    manager = AGENT_MANAGERS.get(provider)
    if manager:
        return manager
    raise HTTPException(status_code=400, detail="Unsupported agent provider")


@app.get("/api/agents/providers")
async def agent_providers():
    return list(AGENT_PROVIDERS.values())


@app.get("/api/agents/sessions")
async def agent_sessions(provider: str | None = None, user: str | None = None):
    if provider:
        return _agent_manager(provider).list(user)
    return {
        provider: manager.list(user) for provider, manager in AGENT_MANAGERS.items()
    }


@app.post("/api/agents/sessions")
async def create_agent_session(config: AgentSessionCreate, user: str | None = None):
    logger.info("Creating {} session cwd={}", config.provider, config.cwd or "")
    return await _agent_manager(config.provider).create(config.prompt, config.cwd, config.model, user)


@app.get("/api/agents/sessions/{session_id}")
async def agent_session(session_id: str, provider: str, detail: str | None = "focus"):
    return _agent_manager(provider).snapshot(session_id, detail)


@app.post("/api/agents/sessions/{session_id}/messages")
async def send_agent_message(session_id: str, message: AgentSessionMessage):
    logger.info("Sending {} message session={}", message.provider, session_id)
    return await _agent_manager(message.provider).send(session_id, message.prompt, message.model)


@app.post("/api/agents/sessions/{session_id}/queue")
async def queue_agent_message(session_id: str, message: AgentQueueMessage):
    logger.info("Queueing {} message session={}", message.provider, session_id)
    return await _agent_manager(message.provider).enqueue(session_id, message.prompt, message.model)


@app.put("/api/agents/sessions/{session_id}/queue/{item_id}")
async def update_agent_queue_message(session_id: str, item_id: str, message: AgentQueueMessage):
    logger.info("Updating queued {} message session={} item={}", message.provider, session_id, item_id)
    return await _agent_manager(message.provider).update_queue_item(session_id, item_id, message.prompt, message.model)


@app.delete("/api/agents/sessions/{session_id}/queue/{item_id}")
async def delete_agent_queue_message(session_id: str, item_id: str, provider: str):
    logger.info("Deleting queued {} message session={} item={}", provider, session_id, item_id)
    return await _agent_manager(provider).delete_queue_item(session_id, item_id)


@app.post("/api/agents/sessions/{session_id}/terminate")
async def terminate_agent_session(session_id: str, message: AgentProviderRequest):
    logger.info("Terminating {} session {}", message.provider, session_id)
    return await _agent_manager(message.provider).terminate(session_id)


@app.post("/api/agents/sessions/{session_id}/approvals/{approval_id}")
async def resolve_agent_approval(session_id: str, approval_id: str, message: AgentApprovalDecision):
    logger.info("Resolving {} approval session={} approval={} choice={}", message.provider, session_id, approval_id, message.choice)
    return await _agent_manager(message.provider).resolve_approval(session_id, approval_id, message.choice, message.all)


@app.websocket("/api/agents/sessions/{session_id}/ws")
async def agent_session_ws(websocket: WebSocket, session_id: str, provider: str, detail: str | None = "focus"):
    await _agent_manager(provider).connect(session_id, websocket, detail)


@app.get("/api/codex/sessions")
async def codex_sessions(user: str | None = None):
    return codex_session_manager.list(user)


@app.get("/api/codex/status", response_model=CodexCliStatus)
async def codex_status():
    return codex_session_manager.cli_status()


@app.get("/api/codex/models", response_model=CodexModelOptions)
async def codex_models():
    return await codex_session_manager.model_options()


@app.post("/api/codex/sessions")
async def create_codex_session(config: CodexSessionCreate, user: str | None = None):
    logger.info("Creating Codex session cwd={}", config.cwd or "")
    return await codex_session_manager.create(config.prompt, config.cwd, config.model, user)


@app.get("/api/codex/sessions/{session_id}")
async def codex_session(session_id: str):
    return codex_session_manager.snapshot(session_id, "full")


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


@app.websocket("/api/codex/sessions/{session_id}/ws")
async def codex_session_ws(websocket: WebSocket, session_id: str):
    await codex_session_manager.connect(session_id, websocket, "full")


@app.get("/api/hermes/sessions")
async def hermes_sessions(user: str | None = None):
    return hermes_session_manager.list(user)


@app.post("/api/hermes/sessions")
async def create_hermes_session(config: HermesSessionCreate, user: str | None = None):
    logger.info("Creating Hermes session cwd={}", config.cwd or "")
    return await hermes_session_manager.create(config.prompt, config.cwd, config.model, user)


@app.get("/api/hermes/sessions/{session_id}")
async def hermes_session(session_id: str):
    return hermes_session_manager.snapshot(session_id, "full")


@app.post("/api/hermes/sessions/{session_id}/messages")
async def send_hermes_message(session_id: str, message: HermesSessionMessage):
    logger.info("Sending Hermes message session={}", session_id)
    return await hermes_session_manager.send(session_id, message.prompt, message.model)


@app.post("/api/hermes/sessions/{session_id}/queue")
async def queue_hermes_message(session_id: str, message: HermesQueueMessage):
    logger.info("Queueing Hermes message session={}", session_id)
    return await hermes_session_manager.enqueue(session_id, message.prompt, message.model)


@app.put("/api/hermes/sessions/{session_id}/queue/{item_id}")
async def update_hermes_queue_message(session_id: str, item_id: str, message: HermesQueueMessage):
    logger.info("Updating queued Hermes message session={} item={}", session_id, item_id)
    return await hermes_session_manager.update_queue_item(session_id, item_id, message.prompt, message.model)


@app.delete("/api/hermes/sessions/{session_id}/queue/{item_id}")
async def delete_hermes_queue_message(session_id: str, item_id: str):
    logger.info("Deleting queued Hermes message session={} item={}", session_id, item_id)
    return await hermes_session_manager.delete_queue_item(session_id, item_id)


@app.post("/api/hermes/sessions/{session_id}/terminate")
async def terminate_hermes_session(session_id: str):
    logger.info("Terminating Hermes session {}", session_id)
    return await hermes_session_manager.terminate(session_id)


@app.websocket("/api/hermes/sessions/{session_id}/ws")
async def hermes_session_ws(websocket: WebSocket, session_id: str):
    await hermes_session_manager.connect(session_id, websocket, "full")


@app.websocket("/api/voice/ws")
async def voice_ws(websocket: WebSocket):
    await connect_voice(websocket)


dist = settings.frontend_dist_resolved
if dist and Path(dist).exists():
    app.mount("/", StaticFiles(directory=dist, html=True), name="frontend")
