import asyncio
from contextlib import contextmanager
import json
import os
import shutil
import sqlite3
import time
import uuid
from pathlib import Path
from typing import Any, Iterator, Literal

from fastapi import HTTPException
from loguru import logger
from pydantic import BaseModel, Field

from .config import settings
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
    agent_session_id: str | None = None
    summary: str | None = None
    decision: str | None = None
    metrics: dict[str, Any] | None = None
    failure_reason: str | None = None
    next_suggestions: list[str] | None = None
    user_decision_needed: str | None = None
    reason: str = ""


class AgentTaskSettingsUpdate(BaseModel):
    default_group_id: str | None = None
    mode: TaskMode | None = None
    default_agent: str | None = None
    default_model: str | None = None
    auto_tick_seconds: int | None = None


class AgentTaskPlanUpdate(BaseModel):
    group_id: str = "default"
    goal: str = ""
    plan: str = ""
    context: str = ""
    constraints: list[str] = Field(default_factory=list)
    reason: str = ""


class AgentTaskDispatchRequest(BaseModel):
    force: bool = False
    agent: str | None = None
    model: str | None = None


class AgentTaskManagerRequest(BaseModel):
    group_id: str = "default"
    task_id: str | None = None
    prompt: str = ""
    reason: str = ""
    trigger: str = "user"
    model: str | None = None


class AgentTaskScopedManagerRequest(BaseModel):
    prompt: str = ""
    reason: str = ""
    trigger: str = "executor_request"
    model: str | None = None


class AgentTaskResetRequest(BaseModel):
    action: Literal["clear", "retry"] = "clear"
    reason: str = ""


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
                    goal TEXT NOT NULL DEFAULT '',
                    plan TEXT NOT NULL DEFAULT '',
                    context TEXT NOT NULL DEFAULT '',
                    constraints_json TEXT NOT NULL DEFAULT '[]',
                    manager_session_id TEXT,
                    updated_at REAL NOT NULL,
                    PRIMARY KEY (user_id, group_id)
                )
                """
            )
            for column, ddl in (
                ("goal", "ALTER TABLE settings ADD COLUMN goal TEXT NOT NULL DEFAULT ''"),
                ("plan", "ALTER TABLE settings ADD COLUMN plan TEXT NOT NULL DEFAULT ''"),
                ("context", "ALTER TABLE settings ADD COLUMN context TEXT NOT NULL DEFAULT ''"),
                ("constraints_json", "ALTER TABLE settings ADD COLUMN constraints_json TEXT NOT NULL DEFAULT '[]'"),
                ("manager_session_id", "ALTER TABLE settings ADD COLUMN manager_session_id TEXT"),
            ):
                existing = [row["name"] for row in conn.execute("PRAGMA table_info(settings)").fetchall()]
                if column not in existing:
                    conn.execute(ddl)
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
            "goal": "",
            "plan": "",
            "context": "",
            "constraints_json": "[]",
            "manager_session_id": None,
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
            "goal": current.get("goal", ""),
            "plan": current.get("plan", ""),
            "context": current.get("context", ""),
            "constraints_json": current.get("constraints_json", "[]"),
            "manager_session_id": current.get("manager_session_id"),
            "updated_at": _now(),
        }
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO settings (
                    user_id, group_id, mode, default_agent, default_model, auto_tick_seconds,
                    goal, plan, context, constraints_json, manager_session_id, updated_at
                )
                VALUES (
                    :user_id, :group_id, :mode, :default_agent, :default_model, :auto_tick_seconds,
                    :goal, :plan, :context, :constraints_json, :manager_session_id, :updated_at
                )
                ON CONFLICT(user_id, group_id) DO UPDATE SET
                    mode=excluded.mode,
                    default_agent=excluded.default_agent,
                    default_model=excluded.default_model,
                    auto_tick_seconds=excluded.auto_tick_seconds,
                    goal=excluded.goal,
                    plan=excluded.plan,
                    context=excluded.context,
                    constraints_json=excluded.constraints_json,
                    manager_session_id=excluded.manager_session_id,
                    updated_at=excluded.updated_at
                """,
                payload,
            )
            conn.commit()
        self._record_event(None, user, group_id, "user", None, "settings_updated", current, payload, "Updated task scheduler settings")
        return payload

    def plan(self, user_id: str | None, group_id: str = "default") -> dict:
        settings = self.settings(user_id, group_id)
        return {
            "user_id": settings["user_id"],
            "group_id": settings["group_id"],
            "goal": settings.get("goal", ""),
            "plan": settings.get("plan", ""),
            "context": settings.get("context", ""),
            "constraints": _loads(settings.get("constraints_json"), []),
            "manager_session_id": settings.get("manager_session_id"),
            "updated_at": settings["updated_at"],
        }

    def update_plan(self, update: AgentTaskPlanUpdate, user_id: str | None, actor: str = "user") -> dict:
        user = normalize_user_id(user_id)
        group_id = update.group_id.strip() or "default"
        current = self.settings(user, group_id)
        before = self.plan(user, group_id)
        next_settings = {
            **current,
            "goal": update.goal,
            "plan": update.plan,
            "context": update.context,
            "constraints_json": _json(update.constraints),
            "updated_at": _now(),
        }
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO settings (
                    user_id, group_id, mode, default_agent, default_model, auto_tick_seconds,
                    goal, plan, context, constraints_json, manager_session_id, updated_at
                )
                VALUES (
                    :user_id, :group_id, :mode, :default_agent, :default_model, :auto_tick_seconds,
                    :goal, :plan, :context, :constraints_json, :manager_session_id, :updated_at
                )
                ON CONFLICT(user_id, group_id) DO UPDATE SET
                    goal=excluded.goal,
                    plan=excluded.plan,
                    context=excluded.context,
                    constraints_json=excluded.constraints_json,
                    updated_at=excluded.updated_at
                """,
                next_settings,
            )
            conn.commit()
        after = self.plan(user, group_id)
        self._record_event(None, user, group_id, actor, current.get("manager_session_id"), "plan_updated", before, after, update.reason)
        return after

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

    def groups(self, user_id: str | None = None) -> list[dict]:
        self._init_db()
        user = normalize_user_id(user_id)
        with self._connect() as conn:
            task_rows = conn.execute(
                """
                SELECT group_id, COUNT(*) AS task_count, MAX(updated_at) AS updated_at
                FROM tasks WHERE user_id=? GROUP BY group_id
                """,
                (user,),
            ).fetchall()
            setting_rows = conn.execute("SELECT * FROM settings WHERE user_id=?", (user,)).fetchall()
        by_group: dict[str, dict] = {}
        for row in task_rows:
            by_group[row["group_id"]] = {
                "user_id": user,
                "group_id": row["group_id"],
                "task_count": row["task_count"],
                "updated_at": row["updated_at"] or 0,
                "goal": "",
                "manager_session_id": None,
                "mode": "manual",
            }
        for row in setting_rows:
            item = by_group.setdefault(
                row["group_id"],
                {
                    "user_id": user,
                    "group_id": row["group_id"],
                    "task_count": 0,
                    "updated_at": row["updated_at"],
                },
            )
            item.update(
                {
                    "goal": row["goal"],
                    "manager_session_id": row["manager_session_id"],
                    "mode": row["mode"],
                    "updated_at": max(float(item.get("updated_at") or 0), float(row["updated_at"] or 0)),
                }
            )
        if not by_group:
            by_group["default"] = {
                "user_id": user,
                "group_id": "default",
                "task_count": 0,
                "updated_at": _now(),
                "goal": "",
                "manager_session_id": None,
                "mode": "manual",
            }
        return sorted(by_group.values(), key=lambda item: (item["updated_at"], item["group_id"]), reverse=True)

    def get(self, task_id: str, user_id: str | None = None) -> dict:
        task = self._get_task(task_id, user_id)
        return task

    def delete(self, task_id: str, user_id: str | None = None) -> dict:
        task = self._get_task(task_id, user_id)
        if task["status"] in {"running", "waiting_process"}:
            raise HTTPException(status_code=409, detail="Cannot delete running or waiting-process tasks")
        with self._connect() as conn:
            dependents = conn.execute(
                "SELECT id, depends_on FROM tasks WHERE user_id=? AND group_id=?",
                (task["user_id"], task["group_id"]),
            ).fetchall()
            blocking = [row["id"] for row in dependents if task_id in _loads(row["depends_on"], [])]
            if blocking:
                raise HTTPException(status_code=409, detail=f"Task is still a dependency of: {', '.join(blocking[:5])}")
            conn.execute("DELETE FROM tasks WHERE id=? AND user_id=?", (task_id, task["user_id"]))
            conn.commit()
        self._record_event(task_id, task["user_id"], task["group_id"], "user", task.get("agent_session_id"), "deleted", task, None, "Deleted task")
        return {"status": "deleted", "task_id": task_id}

    def _downstream_task_ids(self, task: dict) -> list[str]:
        tasks = self.list(task["user_id"], task["group_id"])["tasks"]
        by_id = {item["id"]: item for item in tasks}
        dependents: dict[str, list[str]] = {item["id"]: [] for item in tasks}
        for item in tasks:
            for dep_id in item.get("depends_on", []):
                dependents.setdefault(dep_id, []).append(item["id"])
        affected: list[str] = []
        seen: set[str] = set()
        queue = [task["id"]]
        while queue:
            current = queue.pop(0)
            if current in seen or current not in by_id:
                continue
            seen.add(current)
            affected.append(current)
            queue.extend(dependents.get(current, []))
        return affected

    def _clear_task_storage(self, task_id: str) -> None:
        shutil.rmtree(AGENT_TASK_LOG_DIR / task_id, ignore_errors=True)

    async def reset(self, task_id: str, request: AgentTaskResetRequest, user_id: str | None = None, actor: str = "user") -> dict:
        task = self._get_task(task_id, user_id)
        affected_ids = self._downstream_task_ids(task)
        affected = [self._get_task(item_id, task["user_id"]) for item_id in affected_ids]
        active = [item["id"] for item in affected if item["status"] in {"claimed", "running", "waiting_process"}]
        if active:
            raise HTTPException(status_code=409, detail=f"Cannot reset active tasks: {', '.join(active[:5])}")

        reset_tasks: list[dict] = []
        for item in affected:
            before = dict(item)
            next_task = dict(item)
            next_task["status"] = "backlog"
            next_task["blocked_reason"] = None
            next_task["agent_session_id"] = None
            next_task["runtime"] = AgentTaskRuntime().model_dump()
            next_task["artifacts"] = []
            next_task["result"] = AgentTaskResult().model_dump()
            self._clear_task_storage(item["id"])
            self._write_task(next_task, bump=True)
            after = self._get_task(item["id"], item["user_id"])
            self._record_event(
                item["id"],
                item["user_id"],
                item["group_id"],
                actor,
                before.get("agent_session_id"),
                f"{request.action}_reset",
                before,
                after,
                request.reason or f"{request.action} requested from task DAG page",
            )
            reset_tasks.append(after)

        self.recompute_ready(task["user_id"], task["group_id"])
        reset_tasks = [self._get_task(item_id, task["user_id"]) for item_id in affected_ids]
        dispatched = None
        if request.action == "retry":
            target = self._get_task(task_id, task["user_id"])
            if target["status"] == "ready":
                dispatched = await self.dispatch(task_id, AgentTaskDispatchRequest(force=True), task["user_id"], actor)
            else:
                dispatched = target
        return {
            "action": request.action,
            "affected_task_ids": affected_ids,
            "tasks": reset_tasks,
            "dispatched": dispatched,
        }

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
        return {"task": task, "plan": self.plan(task["user_id"], task["group_id"]), "dependencies": dependencies, "children": children, "ancestors": ancestors, "events": events}

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
        if update.agent_session_id and task.get("agent_session_id") and update.agent_session_id != task.get("agent_session_id"):
            raise HTTPException(status_code=409, detail="Completion does not match the task's active agent session")
        before = dict(task)
        task["status"] = update.status
        task["blocked_reason"] = None
        result = update.result.model_dump()
        if update.summary is not None:
            result["summary"] = update.summary
        if update.decision is not None:
            result["decision"] = update.decision
        if update.metrics is not None:
            result["metrics"] = update.metrics
        if update.failure_reason is not None:
            result["failure_reason"] = update.failure_reason
        if update.next_suggestions is not None:
            result["next_suggestions"] = update.next_suggestions
        if update.user_decision_needed is not None:
            result["user_decision_needed"] = update.user_decision_needed
        task["result"] = result
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
        if task["status"] in {"claimed", "running", "waiting_process", "done", "failed", "cancelled"}:
            raise HTTPException(status_code=409, detail=f"Task is {task['status']} and cannot be dispatched")
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
        runtime["task_workspace"] = self._task_workspace(task["id"]).as_posix()
        task["status"] = "running"
        task["assigned_agent"] = agent
        task["runtime"] = runtime
        artifacts = list(task["artifacts"])
        workspace_artifact = {"type": "task_workspace", "path": runtime["task_workspace"], "label": "task-local workspace"}
        if not any(item.get("type") == "task_workspace" for item in artifacts):
            artifacts.append(workspace_artifact)
        task["artifacts"] = artifacts
        self._write_task(task, bump=True)
        summary = await AGENT_MANAGERS[agent].create("", task["workspace"], model, task["user_id"])
        task = self._get_task(task_id, task["user_id"])
        task["agent_session_id"] = summary["id"]
        self._write_task(task, bump=True)
        prompt = self._dispatch_prompt(task)
        summary = await AGENT_MANAGERS[agent].send(summary["id"], prompt, model)
        task = self._get_task(task_id, task["user_id"])
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

    async def request_manager(self, request: AgentTaskManagerRequest, user_id: str | None = None, actor: str = "user") -> dict:
        user = normalize_user_id(user_id)
        group_id = request.group_id.strip() or "default"
        settings = self.settings(user, group_id)
        plan = self.plan(user, group_id)
        task = self._get_task(request.task_id, user) if request.task_id else None
        tasks = self.list(user, group_id)["tasks"]
        session_id = settings.get("manager_session_id")
        model = request.model or settings.get("default_model")
        if session_id:
            prompt = request.prompt.strip() or "(no prompt)"
            try:
                summary = await codex_session_manager.send(session_id, prompt, model)
            except Exception:
                logger.exception("Failed to resume manager session {}; creating a replacement", session_id)
                prompt = self._manager_prompt(plan, tasks, task, request)
                summary = await codex_session_manager.create(prompt, task.get("workspace") if task else "", model, user)
                session_id = summary["id"]
                self._set_manager_session(user, group_id, session_id)
        else:
            prompt = self._manager_prompt(plan, tasks, task, request)
            summary = await codex_session_manager.create(prompt, task.get("workspace") if task else "", model, user)
            session_id = summary["id"]
            self._set_manager_session(user, group_id, session_id)
        self._record_event(
            task["id"] if task else None,
            user,
            group_id,
            actor,
            session_id,
            "manager_requested",
            None,
            {"prompt": request.prompt, "reason": request.reason, "trigger": request.trigger, "session_id": session_id},
            request.reason or request.prompt,
        )
        return {"manager_session_id": session_id, "session": summary}

    async def request_manager_for_task(self, task_id: str, request: AgentTaskScopedManagerRequest, user_id: str | None = None, actor: str = "executor") -> dict:
        task = self._get_task(task_id, user_id)
        return await self.request_manager(
            AgentTaskManagerRequest(
                group_id=task["group_id"],
                task_id=task["id"],
                prompt=request.prompt,
                reason=request.reason,
                trigger=request.trigger,
                model=request.model or task.get("model"),
            ),
            task["user_id"],
            actor,
        )

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
            task["status"] = "review"
            self._write_task(task, bump=True)
            await self._request_manager_after_process(task)

    async def _auto_dispatch_once(self) -> None:
        with self._connect() as conn:
            groups = conn.execute("SELECT DISTINCT user_id, group_id FROM settings WHERE mode='auto'").fetchall()
        for row in groups:
            await self.dispatch_ready(row["user_id"], row["group_id"], limit=1)

    async def _request_manager_after_process(self, task: dict) -> None:
        prompt = self._process_finished_prompt(task)
        try:
            await self.request_manager(
                AgentTaskManagerRequest(
                    group_id=task["group_id"],
                    task_id=task["id"],
                    prompt=prompt,
                    reason="A task process exited and needs manager review/rescheduling.",
                    trigger="process_exit",
                    model=task.get("model"),
                ),
                task["user_id"],
                "scheduler",
            )
        except Exception as exc:
            logger.exception("Failed to request manager for task {}", task["id"])
            self.update_status(
                task["id"],
                AgentTaskStatusUpdate(status="review", reason=f"Process ended but manager request failed: {exc}"),
                task["user_id"],
                "scheduler",
            )

    def _task_api_base_url(self) -> str:
        try:
            from .files import read_config

            configured = read_config().dag.base_url.strip().rstrip("/")
        except Exception:
            configured = ""
        return configured or f"http://127.0.0.1:{settings.port}"

    def _api_url(self, path: str) -> str:
        return f"{self._task_api_base_url()}{path}"

    def _worker_api_contract(self, task: dict, user_query: str) -> str:
        task_id = task["id"]
        current_session_id = task.get("agent_session_id") or "<current viewer session id>"
        return f"""Worker Task API contract. Use exactly the configured base URL below; do not infer a host or port from memory.
Base URL: {self._task_api_base_url()}

Read context before work:
GET {self._api_url(f"/api/agent-tasks/{task_id}/context{user_query}")}
Response includes task, plan, dependencies, children, ancestors, and recent events.

Ask manager when the graph/plan/shared code needs changes:
POST {self._api_url(f"/api/agent-tasks/{task_id}/manager-request{user_query}")}
JSON body:
{{"prompt":"specific manager request","reason":"why executor cannot safely finish locally","trigger":"executor_request"}}

Register long-running process, then stop this turn:
POST {self._api_url(f"/api/agent-tasks/{task_id}/process{user_query}")}
JSON body:
{{"pid":1234,"process_group_id":1234,"log_path":"/absolute/log/path","expected_outputs":["/absolute/output/path"],"reason":"what is running"}}

Complete this task only after outputs are checked:
POST {self._api_url(f"/api/agent-tasks/{task_id}/complete{user_query}")}
JSON body:
{{"status":"done","agent_session_id":"{current_session_id}","artifacts":[{{"type":"markdown","path":"/absolute/output.md","label":"short label"}}],"result":{{"summary":"concise result","metrics":{{"checked_outputs":1}},"failure_reason":null,"next_suggestions":[],"user_decision_needed":null}},"reason":"finished and verified"}}
Allowed status values: done, failed, review, blocked."""

    def _manager_api_contract(self, plan: dict, user_query: str) -> str:
        group_query = f"group_id={plan['group_id']}{user_query.replace('?', '&')}"
        return f"""Manager Task API contract. Use exactly the configured base URL below; do not infer a host or port from memory.
Base URL: {self._task_api_base_url()}

List DAG:
GET {self._api_url(f"/api/agent-tasks?{group_query}")}

Read task context:
GET {self._api_url(f"/api/agent-tasks/<task_id>/context{user_query}")}

Create task:
POST {self._api_url(f"/api/agent-tasks{user_query}")}
JSON body:
{{"group_id":"{plan['group_id']}","parent_id":null,"title":"clear task title","description":"task contract and expected output","status":"backlog","priority":50,"kind":"research","workspace":"","assigned_agent":"codex","model":null,"depends_on":[],"execution":{{"mode":"agent","instruction":"specific worker instructions","command":null,"cwd":"","env":{{}},"timeout_sec":null}},"artifacts":[],"metadata":{{"reason":"why this task exists"}},"policy":{{"requires_approval":false}}}}

Patch mutable task fields before it runs:
PATCH {self._api_url(f"/api/agent-tasks/<task_id>{user_query}")}
JSON body:
{{"expected_version":1,"title":"updated title","description":"updated contract","status":"backlog","priority":60,"execution":{{"mode":"agent","instruction":"updated instructions","command":null,"cwd":"","env":{{}},"timeout_sec":null}},"metadata":{{"reason":"why changed"}},"reason":"short audit reason"}}

Patch dependencies:
POST {self._api_url(f"/api/agent-tasks/<task_id>/dependencies{user_query}")}
JSON body:
{{"replace":["dependency_task_id"],"reason":"why these dependencies are correct"}}

Update global plan/context:
PUT {self._api_url(f"/api/agent-tasks/plan{user_query}")}
JSON body:
{{"group_id":"{plan['group_id']}","goal":"goal","plan":"numbered plan","context":"stable facts for workers","constraints":["constraint"],"reason":"why updated"}}

Complete or block a task when acting on process review:
POST {self._api_url(f"/api/agent-tasks/<task_id>/complete{user_query}")}
JSON body:
{{"status":"done","result":{{"summary":"what happened","metrics":{{}},"failure_reason":null,"next_suggestions":[],"user_decision_needed":null}},"artifacts":[],"reason":"manager decision"}}

Clear or retry a task and every downstream task that depends on it:
POST {self._api_url(f"/api/agent-tasks/<task_id>/reset{user_query}")}
JSON body:
{{"action":"clear","reason":"why previous outputs should be cleared"}}
Use action="retry" to clear the same downstream set and immediately dispatch the selected task if its dependencies are satisfied."""

    def _dependency_artifacts_block(self, task: dict) -> str:
        lines: list[str] = []
        for dep_id in task.get("depends_on") or []:
            try:
                dep = self._get_task(dep_id, task["user_id"])
            except HTTPException:
                lines.append(f"- {dep_id}: missing dependency record")
                continue
            artifact_parts = [
                f"{item.get('type', 'artifact')} {item.get('path')}"
                for item in dep.get("artifacts", [])
                if item.get("path") and item.get("type") != "task_workspace"
            ]
            suffix = "; ".join(artifact_parts) if artifact_parts else "no output artifacts recorded"
            lines.append(f"- {dep['id']} [{dep['status']}] {dep['title']}: {suffix}")
        return "\n".join(lines) or "- none"

    def _dispatch_prompt(self, task: dict) -> str:
        user_query = f"?user={task['user_id']}"
        plan = self.plan(task["user_id"], task["group_id"])
        task_workspace = task["runtime"].get("task_workspace") or self._task_workspace(task["id"]).as_posix()
        output_artifacts = [item for item in task.get("artifacts", []) if item.get("type") != "task_workspace"]
        output_block = "\n".join(f"- {item.get('type', 'artifact')}: {item.get('path')} ({item.get('label') or 'no label'})" for item in output_artifacts) or "- No predeclared output artifact. Create a clear artifact under the task-local workspace and register it on completion."
        return f"""You are an EXECUTOR agent for Viewer Agent Task {task['id']}.

Role boundary:
- You execute exactly this task.
- You may run commands, start long-running processes, inspect logs, write scripts, create files, modify files, and generate outputs inside this task-local workspace:
  {task_workspace}
- You may read the project/common workspace and call its scripts.
- Do not modify shared project/common source code outside the task-local workspace unless this task explicitly grants that permission.
- Do not create tasks, patch dependencies, rewrite the DAG, or change the global plan.
- If the plan is wrong, dependencies are missing, code must change, or rescheduling is needed, call the manager endpoint instead of changing the graph yourself.

Task title: {task['title']}
Group: {task['group_id']}
Workspace: {task['workspace'] or '(default profile home)'}
Task-local workspace: {task_workspace}

Task Contract:
- Inputs/dependencies:
{self._dependency_artifacts_block(task)}
- Expected outputs:
{output_block}
- Done criteria:
  - Read the task context API before doing work.
  - Produce outputs only inside the task-local workspace unless the task explicitly says otherwise.
  - Check that every registered artifact exists and matches the requested format.
  - Complete with a nested result.summary and result.metrics object so the DAG board can show the outcome.

Global goal:
{plan.get('goal') or '(none set)'}

Global plan:
{plan.get('plan') or '(none set)'}

Global constraints:
{chr(10).join(f'- {item}' for item in plan.get('constraints', [])) or '- none set'}

Description:
{task['description']}

Instruction:
{task['execution'].get('instruction') or task['description'] or task['title']}

{self._worker_api_contract(task, user_query)}
"""

    def _process_finished_prompt(self, task: dict) -> str:
        user_query = f"?user={task['user_id']}"
        return f"""A long-running process recorded for Viewer Agent Task {task['id']} has exited.

You are the MANAGER/PLANNER. Inspect the task artifacts, logs, expected outputs, dependency context, and current DAG. Decide whether to:
- mark the task done/failed/review/blocked
- create retry or follow-up tasks
- patch dependencies of not-yet-running tasks
- update the global plan/context
- edit code only if that is necessary and safer than asking an executor to do it

Use the Viewer task API with {user_query} so updates are applied to the correct user profile.
Configured API base URL: {self._task_api_base_url()}
"""

    def _manager_prompt(self, plan: dict, tasks: list[dict], task: dict | None, request: AgentTaskManagerRequest) -> str:
        user_query = f"?user={plan['user_id']}"
        task_lines = []
        for item in tasks[:80]:
            deps = ",".join(item.get("depends_on") or [])
            task_lines.append(f"- {item['id']} [{item['status']}] p{item['priority']} {item['title']} deps=[{deps}]")
        task_block = "\n".join(task_lines) or "(no tasks yet)"
        focus = f"{task['id']} [{task['status']}] {task['title']}" if task else "(none)"
        return f"""You are the MANAGER/PLANNER for Viewer Agent Task DAG group {plan['group_id']}.

Your role:
- Own the global plan and DAG structure.
- Create, split, cancel, retry, and reschedule tasks.
- Patch dependencies only for not-yet-running tasks.
- Edit source code only when the plan requires code changes; executors should normally not edit files.
- Keep executor tasks narrow: each executor should run or inspect one concrete unit and record PID/results.
- Prefer putting new expensive tasks in backlog/review unless auto execution is explicitly desired.

Current goal:
{plan.get('goal') or '(none set)'}

Current plan:
{plan.get('plan') or '(none set)'}

Project context:
{plan.get('context') or '(none set)'}

Constraints:
{chr(10).join(f'- {item}' for item in plan.get('constraints', [])) or '- none set'}

Current DAG:
{task_block}

Focus task:
{focus}

Request trigger: {request.trigger}
Request reason: {request.reason or '(none)'}
User/request prompt:
{request.prompt or '(none)'}

{self._manager_api_contract(plan, user_query)}

When you make a graph or plan change, include a concise reason. If user approval is needed, set the affected task to review or create a review task.
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

    def _task_workspace(self, task_id: str) -> Path:
        path = AGENT_TASK_LOG_DIR / task_id / "workspace"
        path.mkdir(parents=True, exist_ok=True)
        return path

    def _set_manager_session(self, user_id: str, group_id: str, session_id: str) -> None:
        settings = self.settings(user_id, group_id)
        payload = {**settings, "manager_session_id": session_id, "updated_at": _now()}
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO settings (
                    user_id, group_id, mode, default_agent, default_model, auto_tick_seconds,
                    goal, plan, context, constraints_json, manager_session_id, updated_at
                )
                VALUES (
                    :user_id, :group_id, :mode, :default_agent, :default_model, :auto_tick_seconds,
                    :goal, :plan, :context, :constraints_json, :manager_session_id, :updated_at
                )
                ON CONFLICT(user_id, group_id) DO UPDATE SET
                    manager_session_id=excluded.manager_session_id,
                    updated_at=excluded.updated_at
                """,
                payload,
            )
            conn.commit()

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
