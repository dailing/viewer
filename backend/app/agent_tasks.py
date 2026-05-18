import asyncio
from contextlib import contextmanager
import json
import os
import sqlite3
import time
import uuid
from pathlib import Path
from typing import Any, Iterator, Literal

from fastapi import HTTPException
from loguru import logger
from pydantic import BaseModel, Field

from .codex_sessions import codex_session_manager
from .hermes_sessions import hermes_session_manager
from .storage import AGENT_TASK_LOG_DIR, AGENT_TASKS_DB_PATH, ensure_view_home
from .users import normalize_user_id

TaskStatus = Literal[
    "draft",
    "backlog",
    "ready",
    "claimed",
    "running",
    "waiting_process",
    "review",
    "done",
    "failed",
    "blocked",
    "cancelled",
]
TaskMode = Literal["manual", "auto"]
TASK_STATUSES: tuple[str, ...] = (
    "draft",
    "backlog",
    "ready",
    "claimed",
    "running",
    "waiting_process",
    "review",
    "done",
    "failed",
    "blocked",
    "cancelled",
)
MUTABLE_STATUSES = {"draft", "backlog", "ready", "blocked"}
TERMINAL_STATUSES = {"done", "failed", "cancelled"}
AGENT_MANAGERS = {"codex": codex_session_manager, "hermes": hermes_session_manager}


class AgentTaskExecution(BaseModel):
    mode: Literal["agent", "shell", "manual"] = "agent"
    instruction: str = ""
    command: str | None = None
    cwd: str = ""
    env: dict[str, str] = Field(default_factory=dict)
    timeout_sec: int | None = None


class AgentTaskRuntime(BaseModel):
    pid: int | None = None
    process_group_id: int | None = None
    exit_code: int | None = None
    started_at: float | None = None
    ended_at: float | None = None
    heartbeat_at: float | None = None
    attempt: int = 0
    lease_owner: str | None = None
    lease_expires_at: float | None = None


class AgentTaskArtifact(BaseModel):
    type: str
    path: str
    label: str | None = None


class AgentTaskResult(BaseModel):
    summary: str | None = None
    decision: str | None = None
    metrics: dict[str, Any] = Field(default_factory=dict)
    failure_reason: str | None = None
    next_suggestions: list[str] = Field(default_factory=list)
    user_decision_needed: str | None = None


class AgentTaskPolicy(BaseModel):
    auto_dispatch: bool | None = None
    requires_approval: bool = False
    max_depth: int | None = None
    max_children: int | None = None


class AgentTaskCreate(BaseModel):
    group_id: str = "default"
    parent_id: str | None = None
    title: str
    description: str = ""
    status: TaskStatus = "backlog"
    priority: int = 50
    kind: str = "task"
    workspace: str = ""
    assigned_agent: str = "codex"
    model: str | None = None
    depends_on: list[str] = Field(default_factory=list)
    execution: AgentTaskExecution = Field(default_factory=AgentTaskExecution)
    artifacts: list[AgentTaskArtifact] = Field(default_factory=list)
    metadata: dict[str, Any] = Field(default_factory=dict)
    policy: AgentTaskPolicy = Field(default_factory=AgentTaskPolicy)


class AgentTaskPatch(BaseModel):
    expected_version: int | None = None
    title: str | None = None
    description: str | None = None
    status: TaskStatus | None = None
    priority: int | None = None
    kind: str | None = None
    workspace: str | None = None
    assigned_agent: str | None = None
    model: str | None = None
    depends_on: list[str] | None = None
    execution: AgentTaskExecution | None = None
    artifacts: list[AgentTaskArtifact] | None = None
    result: AgentTaskResult | None = None
    metadata: dict[str, Any] | None = None
    policy: AgentTaskPolicy | None = None
    reason: str = ""


class AgentTaskDependencyPatch(BaseModel):
    add: list[str] = Field(default_factory=list)
    remove: list[str] = Field(default_factory=list)
    replace: list[str] | None = None
    expected_version: int | None = None
    reason: str = ""


class AgentTaskStatusUpdate(BaseModel):
    status: TaskStatus
    reason: str = ""
    result: AgentTaskResult | None = None


class AgentTaskProcessUpdate(BaseModel):
    pid: int
    process_group_id: int | None = None
    log_path: str | None = None
    expected_outputs: list[str] = Field(default_factory=list)
    reason: str = ""


class AgentTaskCompleteUpdate(BaseModel):
    status: Literal["done", "failed", "review", "blocked"] = "done"
    result: AgentTaskResult = Field(default_factory=AgentTaskResult)
    artifacts: list[AgentTaskArtifact] | None = None
    reason: str = ""


class AgentTaskSettingsUpdate(BaseModel):
    default_group_id: str | None = None
    mode: TaskMode | None = None
    default_agent: str | None = None
    default_model: str | None = None
    auto_tick_seconds: int | None = None


class AgentTaskDispatchRequest(BaseModel):
    force: bool = False
    agent: str | None = None
    model: str | None = None


def _json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def _loads(value: str | None, fallback: Any) -> Any:
    if not value:
        return fallback
    try:
        return json.loads(value)
    except json.JSONDecodeError:
        return fallback


def _now() -> float:
    return time.time()


class AgentTaskManager:
    def __init__(self) -> None:
        self.db_path = AGENT_TASKS_DB_PATH
        self._started = False
        self._monitor_task: asyncio.Task | None = None
        self._stop_event: asyncio.Event | None = None

    @contextmanager
    def _connect(self) -> Iterator[sqlite3.Connection]:
        ensure_view_home()
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        conn = sqlite3.connect(self.db_path)
        try:
            conn.row_factory = sqlite3.Row
            conn.execute("PRAGMA foreign_keys = ON")
            yield conn
        except Exception:
            conn.rollback()
            raise
        finally:
            conn.close()

    def _init_db(self) -> None:
        with self._connect() as conn:
            conn.execute(
                """
                CREATE TABLE IF NOT EXISTS tasks (
                    id TEXT PRIMARY KEY,
                    user_id TEXT NOT NULL,
                    group_id TEXT NOT NULL,
                    root_id TEXT,
                    parent_id TEXT,
                    title TEXT NOT NULL,
                    description TEXT NOT NULL,
                    status TEXT NOT NULL,
                    blocked_reason TEXT,
                    priority INTEGER NOT NULL,
                    kind TEXT NOT NULL,
                    workspace TEXT NOT NULL,
                    assigned_agent TEXT NOT NULL,
                    model TEXT,
                    agent_session_id TEXT,
                    depends_on TEXT NOT NULL,
                    execution TEXT NOT NULL,
                    runtime TEXT NOT NULL,
                    artifacts TEXT NOT NULL,
                    result TEXT NOT NULL,
                    metadata TEXT NOT NULL,
                    policy TEXT NOT NULL,
                    version INTEGER NOT NULL,
                    created_by TEXT NOT NULL,
                    created_at REAL NOT NULL,
                    updated_at REAL NOT NULL
                )
                """
            )
            conn.execute(
                """
                CREATE TABLE IF NOT EXISTS events (
                    id TEXT PRIMARY KEY,
                    task_id TEXT,
                    user_id TEXT NOT NULL,
                    group_id TEXT NOT NULL,
                    actor TEXT NOT NULL,
                    agent_session_id TEXT,
                    type TEXT NOT NULL,
                    before_json TEXT,
                    after_json TEXT,
                    reason TEXT NOT NULL,
                    created_at REAL NOT NULL
                )
                """
            )
            conn.execute(
                """
                CREATE TABLE IF NOT EXISTS settings (
                    user_id TEXT NOT NULL,
                    group_id TEXT NOT NULL,
                    mode TEXT NOT NULL,
                    default_agent TEXT NOT NULL,
                    default_model TEXT,
                    auto_tick_seconds INTEGER NOT NULL,
                    updated_at REAL NOT NULL,
                    PRIMARY KEY (user_id, group_id)
                )
                """
            )
            conn.commit()

    async def start(self) -> None:
        if self._started:
            return
        self._init_db()
        self._stop_event = asyncio.Event()
        self._monitor_task = asyncio.create_task(self._monitor_loop())
        self._started = True

    async def shutdown(self) -> None:
        if self._stop_event:
            self._stop_event.set()
        if self._monitor_task:
            self._monitor_task.cancel()
            try:
                await self._monitor_task
            except asyncio.CancelledError:
                pass
        self._started = False

    def settings(self, user_id: str | None, group_id: str = "default") -> dict:
        self._init_db()
        user = normalize_user_id(user_id)
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM settings WHERE user_id=? AND group_id=?", (user, group_id)).fetchone()
            if row:
                return dict(row)
        return {
            "user_id": user,
            "group_id": group_id,
            "mode": "manual",
            "default_agent": "codex",
            "default_model": None,
            "auto_tick_seconds": 10,
            "updated_at": _now(),
        }

    def update_settings(self, update: AgentTaskSettingsUpdate, user_id: str | None) -> dict:
        self._init_db()
        user = normalize_user_id(user_id)
        group_id = (update.default_group_id or "default").strip() or "default"
        current = self.settings(user, group_id)
        mode = update.mode or current["mode"]
        if mode not in ("manual", "auto"):
            raise HTTPException(status_code=400, detail="Invalid task mode")
        default_agent = (update.default_agent or current["default_agent"] or "codex").strip()
        if default_agent not in AGENT_MANAGERS:
            raise HTTPException(status_code=400, detail="Unsupported agent")
        auto_tick_seconds = max(3, int(update.auto_tick_seconds or current["auto_tick_seconds"] or 10))
        payload = {
            "user_id": user,
            "group_id": group_id,
            "mode": mode,
            "default_agent": default_agent,
            "default_model": update.default_model if update.default_model is not None else current["default_model"],
            "auto_tick_seconds": auto_tick_seconds,
            "updated_at": _now(),
        }
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO settings (user_id, group_id, mode, default_agent, default_model, auto_tick_seconds, updated_at)
                VALUES (:user_id, :group_id, :mode, :default_agent, :default_model, :auto_tick_seconds, :updated_at)
                ON CONFLICT(user_id, group_id) DO UPDATE SET
                    mode=excluded.mode,
                    default_agent=excluded.default_agent,
                    default_model=excluded.default_model,
                    auto_tick_seconds=excluded.auto_tick_seconds,
                    updated_at=excluded.updated_at
                """,
                payload,
            )
            conn.commit()
        self._record_event(None, user, group_id, "user", None, "settings_updated", current, payload, "Updated task scheduler settings")
        return payload

    def list(self, user_id: str | None, group_id: str | None = None, status: str | None = None) -> dict:
        self._init_db()
        user = normalize_user_id(user_id)
        self.recompute_ready(user, group_id)
        clauses = ["user_id=?"]
        params: list[Any] = [user]
        if group_id:
            clauses.append("group_id=?")
            params.append(group_id)
        if status:
            clauses.append("status=?")
            params.append(status)
        with self._connect() as conn:
            rows = conn.execute(
                f"SELECT * FROM tasks WHERE {' AND '.join(clauses)} ORDER BY priority DESC, updated_at DESC",
                params,
            ).fetchall()
        tasks = [self._task_from_row(row) for row in rows]
        groups = sorted({task["group_id"] for task in tasks} | ({group_id} if group_id else set()))
        return {
            "tasks": tasks,
            "groups": groups,
            "settings": self.settings(user, group_id or (groups[0] if len(groups) == 1 else "default")),
        }

    def get(self, task_id: str, user_id: str | None = None) -> dict:
        task = self._get_task(task_id, user_id)
        return task

    def context(self, task_id: str, user_id: str | None = None) -> dict:
        task = self._get_task(task_id, user_id)
        tasks = {item["id"]: item for item in self.list(task["user_id"], task["group_id"])["tasks"]}
        dependencies = [tasks[dep] for dep in task["depends_on"] if dep in tasks]
        children = [item for item in tasks.values() if item.get("parent_id") == task_id]
        ancestors = []
        cursor = task
        while cursor.get("parent_id") and cursor["parent_id"] in tasks:
            cursor = tasks[cursor["parent_id"]]
            ancestors.append(cursor)
        events = self.events(task_id, task["user_id"])
        return {"task": task, "dependencies": dependencies, "children": children, "ancestors": ancestors, "events": events}

    def create(self, payload: AgentTaskCreate, user_id: str | None = None, actor: str = "user") -> dict:
        self._init_db()
        user = normalize_user_id(user_id)
        if payload.assigned_agent not in AGENT_MANAGERS:
            raise HTTPException(status_code=400, detail="Unsupported agent")
        task_id = f"task_{uuid.uuid4().hex[:12]}"
        now = _now()
        parent = self._get_task(payload.parent_id, user) if payload.parent_id else None
        root_id = parent["root_id"] or parent["id"] if parent else task_id
        depends_on = self._unique(payload.depends_on)
        row = {
            "id": task_id,
            "user_id": user,
            "group_id": payload.group_id.strip() or "default",
            "root_id": root_id,
            "parent_id": payload.parent_id,
            "title": payload.title.strip() or "Untitled task",
            "description": payload.description,
            "status": payload.status,
            "blocked_reason": None,
            "priority": int(payload.priority),
            "kind": payload.kind.strip() or "task",
            "workspace": payload.workspace,
            "assigned_agent": payload.assigned_agent,
            "model": payload.model,
            "agent_session_id": None,
            "depends_on": _json(depends_on),
            "execution": payload.execution.model_dump_json(),
            "runtime": AgentTaskRuntime().model_dump_json(),
            "artifacts": _json([item.model_dump() for item in payload.artifacts]),
            "result": AgentTaskResult().model_dump_json(),
            "metadata": _json(payload.metadata),
            "policy": payload.policy.model_dump_json(),
            "version": 1,
            "created_by": actor,
            "created_at": now,
            "updated_at": now,
        }
        self._validate_dependencies(row["id"], user, row["group_id"], depends_on)
        row["status"], row["blocked_reason"] = self._normalized_status(row["status"], depends_on, user)
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO tasks (
                    id, user_id, group_id, root_id, parent_id, title, description, status, blocked_reason, priority,
                    kind, workspace, assigned_agent, model, agent_session_id, depends_on, execution, runtime,
                    artifacts, result, metadata, policy, version, created_by, created_at, updated_at
                ) VALUES (
                    :id, :user_id, :group_id, :root_id, :parent_id, :title, :description, :status, :blocked_reason,
                    :priority, :kind, :workspace, :assigned_agent, :model, :agent_session_id, :depends_on, :execution,
                    :runtime, :artifacts, :result, :metadata, :policy, :version, :created_by, :created_at, :updated_at
                )
                """,
                row,
            )
            conn.commit()
        task = self._get_task(task_id, user)
        self._record_event(task_id, user, task["group_id"], actor, None, "created", None, task, "Created task")
        self.recompute_ready(user, task["group_id"])
        return self._get_task(task_id, user)

    def patch(self, task_id: str, patch: AgentTaskPatch, user_id: str | None = None, actor: str = "user") -> dict:
        task = self._get_task(task_id, user_id)
        self._assert_mutable(task)
        if patch.expected_version is not None and patch.expected_version != task["version"]:
            raise HTTPException(status_code=409, detail="Task version conflict")
        before = dict(task)
        next_task = dict(task)
        for key in ("title", "description", "priority", "kind", "workspace", "assigned_agent", "model", "status"):
            value = getattr(patch, key)
            if value is not None:
                next_task[key] = value
        if patch.execution is not None:
            next_task["execution"] = patch.execution.model_dump()
        if patch.artifacts is not None:
            next_task["artifacts"] = [item.model_dump() for item in patch.artifacts]
        if patch.result is not None:
            next_task["result"] = patch.result.model_dump()
        if patch.metadata is not None:
            next_task["metadata"] = patch.metadata
        if patch.policy is not None:
            next_task["policy"] = patch.policy.model_dump()
        if patch.depends_on is not None:
            next_task["depends_on"] = self._unique(patch.depends_on)
        if next_task["assigned_agent"] not in AGENT_MANAGERS:
            raise HTTPException(status_code=400, detail="Unsupported agent")
        self._validate_dependencies(task_id, task["user_id"], next_task["group_id"], next_task["depends_on"])
        next_task["status"], next_task["blocked_reason"] = self._normalized_status(next_task["status"], next_task["depends_on"], task["user_id"])
        self._write_task(next_task, bump=True)
        after = self._get_task(task_id, task["user_id"])
        self._record_event(task_id, task["user_id"], task["group_id"], actor, task.get("agent_session_id"), "patched", before, after, patch.reason)
        self.recompute_ready(task["user_id"], task["group_id"])
        return self._get_task(task_id, task["user_id"])

    def patch_dependencies(self, task_id: str, patch: AgentTaskDependencyPatch, user_id: str | None = None, actor: str = "user") -> dict:
        task = self._get_task(task_id, user_id)
        deps = list(task["depends_on"])
        if patch.replace is not None:
            deps = self._unique(patch.replace)
        else:
            remove = set(patch.remove)
            deps = [dep for dep in deps if dep not in remove]
            deps = self._unique([*deps, *patch.add])
        return self.patch(task_id, AgentTaskPatch(expected_version=patch.expected_version, depends_on=deps, reason=patch.reason), task["user_id"], actor)

    def update_status(self, task_id: str, update: AgentTaskStatusUpdate, user_id: str | None = None, actor: str = "user") -> dict:
        task = self._get_task(task_id, user_id)
        before = dict(task)
        task["status"] = update.status
        task["blocked_reason"] = "waiting_dependencies" if update.status == "blocked" and self._has_unfinished_dependency(task["depends_on"], task["user_id"]) else None
        if update.result is not None:
            task["result"] = update.result.model_dump()
        runtime = dict(task["runtime"])
        if update.status in TERMINAL_STATUSES | {"review"}:
            runtime["ended_at"] = _now()
        task["runtime"] = runtime
        self._write_task(task, bump=True)
        after = self._get_task(task_id, task["user_id"])
        self._record_event(task_id, task["user_id"], task["group_id"], actor, task.get("agent_session_id"), f"status_{update.status}", before, after, update.reason)
        self.recompute_ready(task["user_id"], task["group_id"])
        return after

    def set_process(self, task_id: str, update: AgentTaskProcessUpdate, user_id: str | None = None, actor: str = "agent") -> dict:
        task = self._get_task(task_id, user_id)
        before = dict(task)
        runtime = dict(task["runtime"])
        runtime.update(
            {
                "pid": update.pid,
                "process_group_id": update.process_group_id,
                "started_at": runtime.get("started_at") or _now(),
                "heartbeat_at": _now(),
            }
        )
        task["runtime"] = runtime
        task["status"] = "waiting_process"
        artifacts = list(task["artifacts"])
        if update.log_path:
            artifacts.append({"type": "log", "path": update.log_path, "label": "process log"})
        for path in update.expected_outputs:
            artifacts.append({"type": "expected_output", "path": path, "label": None})
        task["artifacts"] = artifacts
        self._write_task(task, bump=True)
        after = self._get_task(task_id, task["user_id"])
        self._record_event(task_id, task["user_id"], task["group_id"], actor, task.get("agent_session_id"), "process_set", before, after, update.reason)
        return after

    def complete(self, task_id: str, update: AgentTaskCompleteUpdate, user_id: str | None = None, actor: str = "agent") -> dict:
        task = self._get_task(task_id, user_id)
        before = dict(task)
        task["status"] = update.status
        task["blocked_reason"] = None
        task["result"] = update.result.model_dump()
        if update.artifacts is not None:
            task["artifacts"] = [item.model_dump() for item in update.artifacts]
        runtime = dict(task["runtime"])
        runtime["ended_at"] = _now()
        runtime["heartbeat_at"] = _now()
        task["runtime"] = runtime
        self._write_task(task, bump=True)
        after = self._get_task(task_id, task["user_id"])
        self._record_event(task_id, task["user_id"], task["group_id"], actor, task.get("agent_session_id"), f"completed_{update.status}", before, after, update.reason)
        self.recompute_ready(task["user_id"], task["group_id"])
        return after

    async def dispatch(self, task_id: str, request: AgentTaskDispatchRequest | None = None, user_id: str | None = None, actor: str = "user") -> dict:
        task = self._get_task(task_id, user_id)
        if task["status"] != "ready" and not (request and request.force):
            raise HTTPException(status_code=409, detail=f"Task is {task['status']}, not ready")
        if self._has_unfinished_dependency(task["depends_on"], task["user_id"]):
            raise HTTPException(status_code=409, detail="Task has unfinished dependencies")
        agent = (request.agent if request and request.agent else task["assigned_agent"]) or "codex"
        if agent not in AGENT_MANAGERS:
            raise HTTPException(status_code=400, detail="Unsupported agent")
        model = request.model if request and request.model else task.get("model")
        before = dict(task)
        runtime = dict(task["runtime"])
        runtime["started_at"] = _now()
        runtime["attempt"] = int(runtime.get("attempt") or 0) + 1
        runtime["heartbeat_at"] = _now()
        task["status"] = "running"
        task["assigned_agent"] = agent
        task["runtime"] = runtime
        self._write_task(task, bump=True)
        prompt = self._dispatch_prompt(task)
        summary = await AGENT_MANAGERS[agent].create(prompt, task["workspace"], model, task["user_id"])
        task = self._get_task(task_id, task["user_id"])
        task["agent_session_id"] = summary["id"]
        runtime = dict(task["runtime"])
        runtime["pid"] = summary.get("pid")
        task["runtime"] = runtime
        self._write_task(task, bump=True)
        after = self._get_task(task_id, task["user_id"])
        self._record_event(task_id, task["user_id"], task["group_id"], actor, summary["id"], "dispatched", before, after, "Dispatched task to agent")
        return after

    async def dispatch_ready(self, user_id: str | None = None, group_id: str | None = None, limit: int = 1, force: bool = False) -> dict:
        user = normalize_user_id(user_id)
        self.recompute_ready(user, group_id)
        with self._connect() as conn:
            rows = conn.execute(
                """
                SELECT * FROM tasks
                WHERE user_id=? AND status='ready' AND (? IS NULL OR group_id=?)
                ORDER BY priority DESC, updated_at ASC
                LIMIT ?
                """,
                (user, group_id, group_id, limit),
            ).fetchall()
        dispatched = []
        for row in rows:
            task = self._task_from_row(row)
            settings = self.settings(user, task["group_id"])
            policy = task["policy"]
            can_auto = force or (settings["mode"] == "auto" and not policy.get("requires_approval") and policy.get("auto_dispatch", True) is not False)
            if not can_auto:
                continue
            try:
                dispatched.append(await self.dispatch(task["id"], AgentTaskDispatchRequest(force=True), user, "scheduler"))
            except Exception as exc:
                logger.exception("Failed to dispatch task {}", task["id"])
                self.update_status(task["id"], AgentTaskStatusUpdate(status="failed", reason=str(exc)), user, "scheduler")
        return {"dispatched": dispatched}

    def recompute_ready(self, user_id: str | None = None, group_id: str | None = None) -> None:
        self._init_db()
        user = normalize_user_id(user_id)
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT * FROM tasks WHERE user_id=? AND (? IS NULL OR group_id=?)",
                (user, group_id, group_id),
            ).fetchall()
        for row in rows:
            task = self._task_from_row(row)
            if task["status"] not in {"backlog", "ready", "blocked"}:
                continue
            next_status, blocked_reason = self._normalized_status(task["status"], task["depends_on"], task["user_id"])
            if (next_status, blocked_reason) == (task["status"], task.get("blocked_reason")):
                continue
            task["status"] = next_status
            task["blocked_reason"] = blocked_reason
            self._write_task(task, bump=True)

    def events(self, task_id: str | None, user_id: str | None = None, limit: int = 100) -> list[dict]:
        self._init_db()
        user = normalize_user_id(user_id)
        clauses = ["user_id=?"]
        params: list[Any] = [user]
        if task_id:
            clauses.append("task_id=?")
            params.append(task_id)
        params.append(limit)
        with self._connect() as conn:
            rows = conn.execute(
                f"SELECT * FROM events WHERE {' AND '.join(clauses)} ORDER BY created_at DESC LIMIT ?",
                params,
            ).fetchall()
        return [
            {
                **dict(row),
                "before": _loads(row["before_json"], None),
                "after": _loads(row["after_json"], None),
            }
            for row in rows
        ]

    async def _monitor_loop(self) -> None:
        while self._stop_event and not self._stop_event.is_set():
            try:
                await self._monitor_once()
                await self._auto_dispatch_once()
            except Exception:
                logger.exception("Agent task monitor tick failed")
            await asyncio.sleep(5)

    async def _monitor_once(self) -> None:
        self._init_db()
        with self._connect() as conn:
            rows = conn.execute("SELECT * FROM tasks WHERE status='waiting_process'").fetchall()
        for row in rows:
            task = self._task_from_row(row)
            runtime = task["runtime"]
            pid = runtime.get("pid")
            if not pid or self._pid_alive(pid):
                continue
            runtime["ended_at"] = _now()
            runtime["heartbeat_at"] = _now()
            task["runtime"] = runtime
            task["status"] = "running"
            self._write_task(task, bump=True)
            await self._resume_after_process(task)

    async def _auto_dispatch_once(self) -> None:
        with self._connect() as conn:
            groups = conn.execute("SELECT DISTINCT user_id, group_id FROM settings WHERE mode='auto'").fetchall()
        for row in groups:
            await self.dispatch_ready(row["user_id"], row["group_id"], limit=1)

    async def _resume_after_process(self, task: dict) -> None:
        session_id = task.get("agent_session_id")
        agent = task.get("assigned_agent") or "codex"
        if not session_id or agent not in AGENT_MANAGERS:
            self.update_status(
                task["id"],
                AgentTaskStatusUpdate(status="review", reason="Process ended; no resumable agent session was attached."),
                task["user_id"],
                "scheduler",
            )
            return
        prompt = self._process_finished_prompt(task)
        try:
            await AGENT_MANAGERS[agent].send(session_id, prompt, task.get("model"))
            self._record_event(task["id"], task["user_id"], task["group_id"], "scheduler", session_id, "process_resume_sent", None, task, "Process ended; resumed agent session")
        except Exception as exc:
            logger.exception("Failed to resume agent session {} for task {}", session_id, task["id"])
            self.update_status(
                task["id"],
                AgentTaskStatusUpdate(status="review", reason=f"Process ended but resume failed: {exc}"),
                task["user_id"],
                "scheduler",
            )

    def _dispatch_prompt(self, task: dict) -> str:
        user_query = f"?user={task['user_id']}"
        context_url = f"/api/agent-tasks/{task['id']}/context{user_query}"
        complete_url = f"/api/agent-tasks/{task['id']}/complete{user_query}"
        process_url = f"/api/agent-tasks/{task['id']}/process{user_query}"
        return f"""You are executing Viewer Agent Task {task['id']}.

Task title: {task['title']}
Group: {task['group_id']}
Workspace: {task['workspace'] or '(default profile home)'}

Description:
{task['description']}

Instruction:
{task['execution'].get('instruction') or task['description'] or task['title']}

Before doing work, read task context with:
  GET {context_url}

You may create follow-up tasks, patch not-yet-running tasks, and update dependencies through /api/agent-tasks.
If you start a long-running process, write logs under ~/.view/logs/agent-tasks/{task['id']}/ and call:
  POST {process_url}
with pid, process_group_id, log_path, and expected_outputs, then stop your turn.

When finished, call:
  POST {complete_url}
with status done, failed, review, or blocked, artifacts, metrics, and a concise summary.
Do not mark the task done until outputs have been checked.
"""

    def _process_finished_prompt(self, task: dict) -> str:
        user_query = f"?user={task['user_id']}"
        return f"""The long-running process recorded for Viewer Agent Task {task['id']} has exited.

Inspect the task artifacts, logs, and expected outputs. Then call /api/agent-tasks/{task['id']}/complete with:
- status=done if outputs are valid
- status=failed if the run failed
- status=review if a user decision is needed
- status=blocked if more input is required

You may create or patch dependent tasks if the result changes the plan.
Use the Viewer task API with {user_query} so updates are applied to the correct user profile.
"""

    def _get_task(self, task_id: str | None, user_id: str | None = None) -> dict:
        if not task_id:
            raise HTTPException(status_code=404, detail="Task not found")
        self._init_db()
        user = normalize_user_id(user_id)
        with self._connect() as conn:
            row = conn.execute("SELECT * FROM tasks WHERE id=? AND user_id=?", (task_id, user)).fetchone()
        if not row:
            raise HTTPException(status_code=404, detail="Task not found")
        return self._task_from_row(row)

    def _task_from_row(self, row: sqlite3.Row) -> dict:
        task = dict(row)
        task["depends_on"] = _loads(task["depends_on"], [])
        task["execution"] = _loads(task["execution"], {})
        task["runtime"] = _loads(task["runtime"], {})
        task["artifacts"] = _loads(task["artifacts"], [])
        task["result"] = _loads(task["result"], {})
        task["metadata"] = _loads(task["metadata"], {})
        task["policy"] = _loads(task["policy"], {})
        return task

    def _write_task(self, task: dict, bump: bool) -> None:
        task = dict(task)
        task["updated_at"] = _now()
        if bump:
            task["version"] = int(task["version"]) + 1
        row = {
            **task,
            "depends_on": _json(task["depends_on"]),
            "execution": _json(task["execution"]),
            "runtime": _json(task["runtime"]),
            "artifacts": _json(task["artifacts"]),
            "result": _json(task["result"]),
            "metadata": _json(task["metadata"]),
            "policy": _json(task["policy"]),
        }
        with self._connect() as conn:
            conn.execute(
                """
                UPDATE tasks SET
                    group_id=:group_id, root_id=:root_id, parent_id=:parent_id, title=:title, description=:description,
                    status=:status, blocked_reason=:blocked_reason, priority=:priority, kind=:kind, workspace=:workspace,
                    assigned_agent=:assigned_agent, model=:model, agent_session_id=:agent_session_id, depends_on=:depends_on,
                    execution=:execution, runtime=:runtime, artifacts=:artifacts, result=:result, metadata=:metadata,
                    policy=:policy, version=:version, updated_at=:updated_at
                WHERE id=:id AND user_id=:user_id
                """,
                row,
            )
            conn.commit()

    def _record_event(
        self,
        task_id: str | None,
        user_id: str,
        group_id: str,
        actor: str,
        agent_session_id: str | None,
        event_type: str,
        before: Any,
        after: Any,
        reason: str,
    ) -> None:
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO events (id, task_id, user_id, group_id, actor, agent_session_id, type, before_json, after_json, reason, created_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    f"evt_{uuid.uuid4().hex[:12]}",
                    task_id,
                    user_id,
                    group_id,
                    actor,
                    agent_session_id,
                    event_type,
                    _json(before) if before is not None else None,
                    _json(after) if after is not None else None,
                    reason,
                    _now(),
                ),
            )
            conn.commit()

    def _assert_mutable(self, task: dict) -> None:
        if task["status"] not in MUTABLE_STATUSES:
            raise HTTPException(status_code=409, detail=f"Task status {task['status']} is not plan-mutable")

    def _validate_dependencies(self, task_id: str, user_id: str, group_id: str, depends_on: list[str]) -> None:
        if task_id in depends_on:
            raise HTTPException(status_code=400, detail="Task cannot depend on itself")
        with self._connect() as conn:
            rows = conn.execute(
                "SELECT id, group_id, depends_on FROM tasks WHERE user_id=?",
                (user_id,),
            ).fetchall()
        graph = {row["id"]: _loads(row["depends_on"], []) for row in rows}
        groups = {row["id"]: row["group_id"] for row in rows}
        for dep in depends_on:
            if dep not in graph:
                raise HTTPException(status_code=400, detail=f"Dependency not found: {dep}")
            if groups[dep] != group_id:
                raise HTTPException(status_code=400, detail=f"Dependency {dep} is in another group")
        graph[task_id] = depends_on
        seen: set[str] = set()
        stack: set[str] = set()

        def visit(node: str) -> bool:
            if node in stack:
                return True
            if node in seen:
                return False
            seen.add(node)
            stack.add(node)
            for dep_id in graph.get(node, []):
                if visit(dep_id):
                    return True
            stack.remove(node)
            return False

        if visit(task_id):
            raise HTTPException(status_code=400, detail="Dependency cycle detected")

    def _normalized_status(self, current: str, depends_on: list[str], user_id: str) -> tuple[str, str | None]:
        failed = self._has_failed_dependency(depends_on, user_id)
        if failed:
            return "blocked", "failed_dependency"
        if self._has_unfinished_dependency(depends_on, user_id):
            return "blocked", "waiting_dependencies"
        if current in {"backlog", "blocked"}:
            return "ready", None
        return current, None

    def _has_unfinished_dependency(self, depends_on: list[str], user_id: str) -> bool:
        if not depends_on:
            return False
        with self._connect() as conn:
            rows = conn.execute(
                f"SELECT status FROM tasks WHERE user_id=? AND id IN ({','.join('?' for _ in depends_on)})",
                [user_id, *depends_on],
            ).fetchall()
        statuses = [row["status"] for row in rows]
        return len(statuses) != len(depends_on) or any(status != "done" for status in statuses)

    def _has_failed_dependency(self, depends_on: list[str], user_id: str) -> bool:
        if not depends_on:
            return False
        with self._connect() as conn:
            rows = conn.execute(
                f"SELECT status FROM tasks WHERE user_id=? AND id IN ({','.join('?' for _ in depends_on)})",
                [user_id, *depends_on],
            ).fetchall()
        return any(row["status"] in {"failed", "cancelled"} for row in rows)

    def _pid_alive(self, pid: int) -> bool:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return False
        except PermissionError:
            return True
        return True

    def _unique(self, values: list[str]) -> list[str]:
        seen: set[str] = set()
        result = []
        for value in values:
            item = str(value).strip()
            if item and item not in seen:
                seen.add(item)
                result.append(item)
        return result


agent_task_manager = AgentTaskManager()
