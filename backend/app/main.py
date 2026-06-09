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

from .agent_loops import agent_loop_manager
from .agent_history import SuperHistoryRunCreate, agent_history_store
from .agent_tasks import (
    AgentTaskCompleteUpdate,
    AgentTaskCreate,
    AgentTaskDependencyPatch,
    AgentTaskDispatchRequest,
    AgentTaskManagerRequest,
    AgentTaskPatch,
    AgentTaskPlanUpdate,
    AgentTaskProcessUpdate,
    AgentTaskResetRequest,
    AgentTaskSettingsUpdate,
    AgentTaskScopedManagerRequest,
    AgentTaskStatusUpdate,
    agent_task_manager,
)
from .config import settings
from .codex_sessions import codex_session_manager
from .conventional_workspace import conventional_workspace_agents
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
from .logging import current_log_path, ensure_logging
from .models import AgentApprovalDecision, AgentLoopCreate, AgentLoopDefinition, AgentLoopRunRequest, AgentProviderRequest, AgentQueueMessage, AgentSessionCreate, AgentSessionMessage, ClientLog, CodexCliStatus, CodexModelOptions, ConfigData, GitCommitRequest, GitRevertRequest, GitStageRequest, TerminalCreate
from .profiling import api_profiler
from .restart import request_restart, request_stop
from .super_workspace import SuperChatCreate, SuperChatPatch, SuperDispatchRequest, SuperRoleCreate, SuperRolePatch, SuperWorkspacePatch, super_workspace_manager
from .super_workspace_runtime import SuperWorkspaceMessageCreate, super_workspace_runtime
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
app.add_middleware(GZipMiddleware, minimum_size=1024)

AGENT_PROVIDERS = {
    "codex": {"id": "codex", "name": "Codex", "icon": "bi-stars"},
    "hermes": {"id": "hermes", "name": "Hermes", "icon": "bi-lightning-charge"},
}

NOISY_SUCCESS_PATHS = {
    "/api/agents/sessions",
    "/api/codex/models",
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
        api_profiler.record(request, 500, elapsed_ms)
        logger.exception("HTTP {} {} failed", request.method, request.url.path)
        raise
    elapsed_ms = (time.perf_counter() - started) * 1000
    api_profiler.record(request, response.status_code, elapsed_ms)
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
    await agent_task_manager.start()
    await super_workspace_runtime.start()


@app.on_event("shutdown")
async def shutdown() -> None:
    logger.info("Shutting down viewer")
    if watch_stop_event:
        watch_stop_event.set()
    if watch_task:
        watch_task.cancel()
    await super_workspace_runtime.shutdown()
    await agent_task_manager.shutdown()
    await agent_loop_manager.shutdown()
    await terminal_manager.shutdown()
    await codex_session_manager.shutdown()
    await hermes_session_manager.shutdown()


@app.get("/api/health")
async def health() -> dict[str, str | int]:
    return {"status": "ok", "root": settings.root_resolved.as_posix(), "pid": os.getpid()}


@app.get("/profile", response_class=HTMLResponse)
async def profile_report() -> HTMLResponse:
    return HTMLResponse(api_profiler.html_report())


@app.get("/api/profile")
async def profile_data():
    return api_profiler.snapshot()


@app.post("/api/profile/reset")
async def reset_profile_data() -> dict[str, str]:
    api_profiler.reset()
    return {"status": "reset"}


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
async def file_site_query(path: str, h: str | None = None, user: str | None = None):
    return file_site_response(path, h, user)


@app.get("/api/file/site/{path:path}")
async def file_site(path: str, h: str | None = None, user: str | None = None):
    return file_site_response(path, h, user)


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
            voice=config.voice,
            dag=config.dag,
            workspace=config.workspace,
            users=config.users or current.users,
            default_user=config.default_user or current.default_user,
        )
    )


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


@app.get("/api/agent-tasks")
async def agent_tasks(group_id: str | None = None, status: str | None = None, user: str | None = None):
    return agent_task_manager.list(user, group_id, status)


@app.get("/api/agent-tasks/groups")
async def agent_task_groups(user: str | None = None):
    return agent_task_manager.groups(user)


@app.get("/api/agent-tasks/settings")
async def agent_task_settings(group_id: str = "default", user: str | None = None):
    return agent_task_manager.settings(user, group_id)


@app.put("/api/agent-tasks/settings")
async def update_agent_task_settings(update: AgentTaskSettingsUpdate, user: str | None = None):
    return agent_task_manager.update_settings(update, user)


@app.get("/api/agent-tasks/plan")
async def agent_task_plan(group_id: str = "default", user: str | None = None):
    return agent_task_manager.plan(user, group_id)


@app.put("/api/agent-tasks/plan")
async def update_agent_task_plan(update: AgentTaskPlanUpdate, user: str | None = None):
    return agent_task_manager.update_plan(update, user)


@app.post("/api/agent-tasks/manager")
async def request_agent_task_manager(request: AgentTaskManagerRequest, user: str | None = None):
    return await agent_task_manager.request_manager(request, user)


@app.post("/api/agent-tasks/dispatch-ready")
async def dispatch_ready_agent_tasks(group_id: str | None = None, limit: int = Query(default=1, ge=1, le=10), force: bool = False, user: str | None = None):
    return await agent_task_manager.dispatch_ready(user, group_id, limit, force)


@app.post("/api/agent-tasks")
async def create_agent_task(task: AgentTaskCreate, user: str | None = None):
    return agent_task_manager.create(task, user)


@app.get("/api/agent-tasks/{task_id}")
async def get_agent_task(task_id: str, user: str | None = None):
    return agent_task_manager.get(task_id, user)


@app.delete("/api/agent-tasks/{task_id}")
async def delete_agent_task(task_id: str, user: str | None = None):
    return agent_task_manager.delete(task_id, user)


@app.post("/api/agent-tasks/{task_id}/reset")
async def reset_agent_task(task_id: str, request: AgentTaskResetRequest, user: str | None = None):
    return await agent_task_manager.reset(task_id, request, user)


@app.get("/api/agent-tasks/{task_id}/context")
async def get_agent_task_context(task_id: str, user: str | None = None):
    return agent_task_manager.context(task_id, user)


@app.get("/api/agent-tasks/{task_id}/files")
async def get_agent_task_files(task_id: str, limit: int = Query(default=200, ge=1, le=1000), user: str | None = None):
    return agent_task_manager.files(task_id, user, limit)


@app.get("/api/agent-tasks/{task_id}/events")
async def get_agent_task_events(task_id: str, limit: int = Query(default=100, ge=1, le=500), user: str | None = None):
    return agent_task_manager.events(task_id, user, limit)


@app.patch("/api/agent-tasks/{task_id}")
async def patch_agent_task(task_id: str, patch: AgentTaskPatch, user: str | None = None):
    return agent_task_manager.patch(task_id, patch, user)


@app.post("/api/agent-tasks/{task_id}/dependencies")
async def patch_agent_task_dependencies(task_id: str, patch: AgentTaskDependencyPatch, user: str | None = None):
    return agent_task_manager.patch_dependencies(task_id, patch, user)


@app.post("/api/agent-tasks/{task_id}/status")
async def update_agent_task_status(task_id: str, update: AgentTaskStatusUpdate, user: str | None = None):
    return agent_task_manager.update_status(task_id, update, user)


@app.post("/api/agent-tasks/{task_id}/process")
async def set_agent_task_process(task_id: str, update: AgentTaskProcessUpdate, user: str | None = None):
    return agent_task_manager.set_process(task_id, update, user)


@app.post("/api/agent-tasks/{task_id}/manager-request")
async def request_agent_task_manager_for_task(task_id: str, request: AgentTaskScopedManagerRequest, user: str | None = None):
    return await agent_task_manager.request_manager_for_task(task_id, request, user)


@app.post("/api/agent-tasks/{task_id}/complete")
async def complete_agent_task(task_id: str, update: AgentTaskCompleteUpdate, user: str | None = None):
    return agent_task_manager.complete(task_id, update, user)


@app.post("/api/agent-tasks/{task_id}/dispatch")
async def dispatch_agent_task(task_id: str, request: AgentTaskDispatchRequest | None = None, user: str | None = None):
    return await agent_task_manager.dispatch(task_id, request, user)


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
    return conventional_workspace_agents.manager(provider)


@app.get("/api/agents/providers")
async def agent_providers():
    return list(AGENT_PROVIDERS.values())


@app.get("/api/super-workspace")
async def super_workspace(user: str | None = None):
    return super_workspace_manager.read(user)


@app.put("/api/super-workspace")
async def update_super_workspace(request: SuperWorkspacePatch, user: str | None = None):
    return super_workspace_manager.update(request, user)


@app.get("/api/super-workspace/workspaces")
async def super_workspaces(user: str | None = None):
    return super_workspace_manager.list_workspaces(user)


@app.post("/api/super-workspace/active-workspace/{workspace_id}")
async def activate_super_workspace(workspace_id: str, user: str | None = None):
    return super_workspace_manager.activate_workspace(workspace_id, user)


@app.get("/api/super-workspace/role-statuses/{workspace_id}")
async def super_role_statuses(workspace_id: str, user: str | None = None):
    return super_workspace_manager.role_statuses(workspace_id, user)


@app.get("/api/super-workspace/chats")
async def super_chats(user: str | None = None):
    return super_workspace_manager.list_chats(user)


@app.post("/api/super-workspace/chats")
async def create_super_chat(request: SuperChatCreate, user: str | None = None):
    return super_workspace_manager.create_chat(request, user)


@app.put("/api/super-workspace/chats/{chat_id}")
async def update_super_chat(chat_id: str, request: SuperChatPatch, user: str | None = None):
    return super_workspace_manager.update_chat(chat_id, request, user)


@app.delete("/api/super-workspace/chats/{chat_id}")
async def delete_super_chat(chat_id: str, user: str | None = None):
    return super_workspace_manager.delete_chat(chat_id, user)


@app.post("/api/super-workspace/active-chat/{chat_id}")
async def activate_super_chat(chat_id: str, user: str | None = None):
    return super_workspace_manager.activate_chat(chat_id, user)


@app.post("/api/super-workspace/roles")
async def create_super_role(request: SuperRoleCreate, user: str | None = None):
    return super_workspace_manager.create_role(request, user)


@app.put("/api/super-workspace/roles/{role_id}")
async def update_super_role(role_id: str, request: SuperRolePatch, user: str | None = None):
    return super_workspace_manager.update_role(role_id, request, user)


@app.delete("/api/super-workspace/roles/{role_id}")
async def delete_super_role(role_id: str, user: str | None = None):
    return super_workspace_manager.delete_role(role_id, user)


@app.get("/api/super-workspace/runs")
async def super_workspace_runs(
    limit: int = Query(default=30, ge=1, le=100),
    before: float | None = None,
    after: float | None = None,
    chat_id: str | None = None,
    user: str | None = None,
):
    return agent_history_store.list_super_display_items(user, limit=limit, before=before, after=after, chat_id=chat_id)


@app.post("/api/super-workspace/messages")
async def create_super_workspace_message(request: SuperWorkspaceMessageCreate, user: str | None = None):
    return await super_workspace_runtime.submit(request, user)


@app.get("/api/super-workspace/events")
async def super_workspace_events(user: str | None = None):
    return StreamingResponse(super_workspace_runtime.event_hub.subscribe(user), media_type="text/event-stream")


@app.post("/internal/super-workspace/notify")
async def notify_super_workspace(event: dict):
    await super_workspace_runtime.notify(event)
    return {"ok": True}


@app.post("/api/super-workspace/runs")
async def create_super_workspace_run(request: SuperHistoryRunCreate, user: str | None = None):
    message = request.message.strip()
    if not message:
        raise HTTPException(status_code=400, detail="Message is required")
    return await super_workspace_runtime.submit(
        SuperWorkspaceMessageCreate(
            message=message,
            chat_id=request.chat_id,
            role_ids=request.role_ids,
            parent_message_id=request.parent_message_id,
            sender_role_id=request.sender_role_id,
        ),
        user,
    )


@app.post("/api/super-workspace/dispatch")
async def dispatch_super_workspace(request: SuperDispatchRequest, user: str | None = None):
    return await super_workspace_manager.dispatch(request, user)


@app.get("/api/agents/sessions")
async def agent_sessions(provider: str | None = None, user: str | None = None):
    if provider:
        return conventional_workspace_agents.list_sessions(provider, user)
    return {
        provider: conventional_workspace_agents.list_sessions(provider, user) for provider in conventional_workspace_agents.providers
    }


@app.post("/api/agents/sessions")
async def create_agent_session(config: AgentSessionCreate, user: str | None = None):
    logger.info("Creating {} session cwd={}", config.provider, config.cwd or "")
    return await conventional_workspace_agents.create_session(
        provider=config.provider,
        prompt=config.prompt,
        cwd=config.cwd,
        model=config.model,
        user=user,
    )


@app.get("/api/agents/sessions/{session_id}")
async def agent_session(session_id: str, provider: str, detail: str | None = "focus"):
    return _agent_manager(provider).snapshot(session_id, detail)


@app.post("/api/agents/sessions/{session_id}/messages")
async def send_agent_message(session_id: str, message: AgentSessionMessage, user: str | None = None):
    logger.info("Sending {} message session={}", message.provider, session_id)
    return await conventional_workspace_agents.dispatch_turn(message.provider, session_id, message.prompt, message.model, user)


@app.post("/api/agents/sessions/{session_id}/queue")
async def queue_agent_message(session_id: str, message: AgentQueueMessage, user: str | None = None):
    logger.info("Queueing {} message session={}", message.provider, session_id)
    return await conventional_workspace_agents.dispatch_turn(message.provider, session_id, message.prompt, message.model, user)


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


@app.get("/api/codex/status", response_model=CodexCliStatus)
async def codex_status():
    return codex_session_manager.cli_status()


@app.get("/api/codex/models", response_model=CodexModelOptions)
async def codex_models():
    return await codex_session_manager.model_options()


@app.websocket("/api/voice/ws")
async def voice_ws(websocket: WebSocket):
    await connect_voice(websocket)


dist = settings.frontend_dist_resolved
if dist and Path(dist).exists():
    app.mount("/", StaticFiles(directory=dist, html=True), name="frontend")
