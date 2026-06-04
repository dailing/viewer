import asyncio
from datetime import datetime, time as dt_time, timedelta, timezone
import json
import re
import time
import uuid
from pathlib import Path
from typing import Any
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from fastapi import HTTPException
from loguru import logger
from pydantic import ValidationError

from .codex_sessions import codex_session_manager
from .models import AgentLoopCreate, AgentLoopDefinition, AgentLoopInfo, AgentLoopRunRecord, AgentLoopRuntime
from .storage import AGENT_LOOP_LOG_DIR, LOOP_STATE_PATH, LOOPS_DIR, ensure_view_home

DEFAULT_TIMEZONE = "Asia/Shanghai"
FRONTMATTER_RE = re.compile(r"\A---\s*\n(.*?)\n---\s*(?:\n|\Z)(.*)\Z", re.DOTALL)
TASK_ID_RE = re.compile(r"^[A-Za-z0-9_.-]+$")


def _slug(value: str) -> str:
    slug = re.sub(r"[^A-Za-z0-9_.-]+", "-", value.strip()).strip("-").lower()
    return slug or "loop-task"


def _local_zone(name: str | None) -> ZoneInfo:
    try:
        return ZoneInfo(name or DEFAULT_TIMEZONE)
    except ZoneInfoNotFoundError:
        return ZoneInfo(DEFAULT_TIMEZONE)


def _parse_scalar(value: str) -> Any:
    raw = value.strip()
    if raw in {"", "null", "None", "~"}:
        return None
    if raw in {"true", "True"}:
        return True
    if raw in {"false", "False"}:
        return False
    if (raw.startswith('"') and raw.endswith('"')) or (raw.startswith("'") and raw.endswith("'")):
        try:
            return json.loads(raw) if raw.startswith('"') else raw[1:-1]
        except json.JSONDecodeError:
            return raw[1:-1]
    if raw.startswith("[") and raw.endswith("]"):
        try:
            return json.loads(raw)
        except json.JSONDecodeError:
            return []
    try:
        return int(raw)
    except ValueError:
        pass
    try:
        return float(raw)
    except ValueError:
        return raw


def _parse_yaml_subset(text: str) -> dict[str, Any]:
    root: dict[str, Any] = {}
    stack: list[tuple[int, dict[str, Any]]] = [(-1, root)]
    for raw_line in text.splitlines():
        if not raw_line.strip() or raw_line.lstrip().startswith("#"):
            continue
        indent = len(raw_line) - len(raw_line.lstrip(" "))
        line = raw_line.strip()
        if ":" not in line:
            continue
        key, value = line.split(":", 1)
        key = key.strip()
        while stack and indent <= stack[-1][0]:
            stack.pop()
        current = stack[-1][1]
        if value.strip() == "":
            child: dict[str, Any] = {}
            current[key] = child
            stack.append((indent, child))
        else:
            current[key] = _parse_scalar(value)
    return root


def _yaml_scalar(value: Any) -> str:
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        return str(value)
    if isinstance(value, list):
        return json.dumps(value, ensure_ascii=True)
    return json.dumps(str(value), ensure_ascii=True)


def _write_yaml_subset(data: dict[str, Any], indent: int = 0) -> list[str]:
    lines: list[str] = []
    prefix = " " * indent
    for key, value in data.items():
        if isinstance(value, dict):
            lines.append(f"{prefix}{key}:")
            lines.extend(_write_yaml_subset(value, indent + 2))
        else:
            lines.append(f"{prefix}{key}: {_yaml_scalar(value)}")
    return lines


def _definition_to_frontmatter(definition: AgentLoopDefinition) -> str:
    data = definition.model_dump(exclude={"prompt"})
    return "\n".join(["---", *_write_yaml_subset(data), "---", "", definition.prompt.rstrip(), ""])


def _definition_from_markdown(path: Path) -> AgentLoopDefinition:
    text = path.read_text(encoding="utf-8")
    match = FRONTMATTER_RE.match(text)
    if not match:
        raise ValueError("Loop task file must start with YAML frontmatter")
    data = _parse_yaml_subset(match.group(1))
    data["prompt"] = match.group(2).strip()
    if "id" not in data:
        data["id"] = path.stem
    if "name" not in data:
        data["name"] = data["id"]
    return AgentLoopDefinition.model_validate(data)


def _timestamp_from_local(value: str | None, zone: ZoneInfo) -> float | None:
    if not value:
        return None
    try:
        parsed = datetime.fromisoformat(value)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=zone)
    return parsed.astimezone(timezone.utc).timestamp()


def _time_from_local(value: str) -> dt_time | None:
    try:
        hour, minute = value.split(":", 1)
        return dt_time(int(hour), int(minute[:2]))
    except (ValueError, TypeError):
        return None


def _next_daily(now: float, zone: ZoneInfo, values: list[str]) -> float | None:
    times = sorted(item for item in (_time_from_local(value) for value in values) if item is not None)
    if not times:
        return None
    local_now = datetime.fromtimestamp(now, tz=timezone.utc).astimezone(zone)
    for day_offset in range(0, 8):
        day = local_now.date() + timedelta(days=day_offset)
        for item in times:
            candidate = datetime.combine(day, item, tzinfo=zone)
            if candidate.timestamp() > now:
                return candidate.astimezone(timezone.utc).timestamp()
    return None


class AgentLoopManager:
    def __init__(self) -> None:
        self._task: asyncio.Task | None = None
        self._stop_event: asyncio.Event | None = None
        self._state: dict[str, AgentLoopRuntime] = {}
        self._definitions: dict[str, AgentLoopDefinition] = {}
        self._parse_errors: dict[str, str] = {}
        self._loaded = False
        self._lock = asyncio.Lock()

    async def start(self) -> None:
        self._ensure_loaded()
        self._stop_event = asyncio.Event()
        self._task = asyncio.create_task(self._scheduler())
        logger.info("Agent loop scheduler started definitions={}", len(self._definitions))

    async def shutdown(self) -> None:
        if self._stop_event:
            self._stop_event.set()
        if self._task:
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
        self._write_state()

    def _ensure_loaded(self) -> None:
        if self._loaded:
            return
        ensure_view_home()
        self._load_state()
        self._load_definitions()
        self._loaded = True

    def _load_state(self) -> None:
        self._state = {}
        if not LOOP_STATE_PATH.exists():
            return
        try:
            raw = json.loads(LOOP_STATE_PATH.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            logger.warning("Ignoring invalid agent loop state {}", LOOP_STATE_PATH)
            return
        for task_id, value in dict(raw.get("tasks") or {}).items():
            try:
                runtime = AgentLoopRuntime.model_validate(value)
                if runtime.current_run_id:
                    runtime.last_status = "failed"
                    runtime.last_error = "Server restarted while run was active"
                    runtime.current_run_id = None
                    runtime.current_trigger = None
                self._state[str(task_id)] = runtime
            except ValidationError:
                logger.warning("Ignoring invalid agent loop runtime for {}", task_id)

    def _write_state(self) -> None:
        ensure_view_home()
        LOOP_STATE_PATH.write_text(
            json.dumps({"tasks": {task_id: runtime.model_dump() for task_id, runtime in self._state.items()}}, indent=2),
            encoding="utf-8",
        )

    def _load_definitions(self) -> None:
        ensure_view_home()
        self._definitions = {}
        self._parse_errors = {}
        for path in sorted(LOOPS_DIR.glob("*.md")):
            try:
                definition = _definition_from_markdown(path)
                self._validate_id(definition.id)
                self._definitions[definition.id] = definition
                self._state.setdefault(definition.id, AgentLoopRuntime())
                self._state[definition.id].next_run_at = self._compute_next_run(definition, self._state[definition.id], time.time())
            except Exception as exc:
                logger.warning("Failed to load loop task {}: {}", path, exc)
                self._parse_errors[path.stem] = str(exc)
        self._write_state()

    def _validate_id(self, task_id: str) -> None:
        if not TASK_ID_RE.match(task_id):
            raise HTTPException(status_code=400, detail="Loop task id may only contain letters, numbers, dots, underscores, and dashes")

    def _path_for(self, task_id: str) -> Path:
        self._validate_id(task_id)
        return LOOPS_DIR / f"{task_id}.md"

    def list(self) -> list[dict]:
        self._ensure_loaded()
        return [self._info(definition).model_dump() for definition in sorted(self._definitions.values(), key=lambda item: item.name.lower())]

    def get(self, task_id: str) -> dict:
        self._ensure_loaded()
        definition = self._definitions.get(task_id)
        if not definition:
            raise HTTPException(status_code=404, detail="Loop task not found")
        return self._info(definition).model_dump()

    def _info(self, definition: AgentLoopDefinition) -> AgentLoopInfo:
        runtime = self._state.setdefault(definition.id, AgentLoopRuntime())
        return AgentLoopInfo(definition=definition, runtime=runtime, path=self._path_for(definition.id).as_posix())

    def create(self, request: AgentLoopCreate) -> dict:
        self._ensure_loaded()
        base = _slug(request.name)
        task_id = base
        index = 2
        while task_id in self._definitions or self._path_for(task_id).exists():
            task_id = f"{base}-{index}"
            index += 1
        definition = AgentLoopDefinition(id=task_id, name=request.name.strip() or "New Loop Task")
        self._write_definition(definition)
        self._definitions[task_id] = definition
        self._state[task_id] = AgentLoopRuntime(next_run_at=self._compute_next_run(definition, AgentLoopRuntime(), time.time()))
        self._write_state()
        logger.info("Created agent loop task id={}", task_id)
        return self._info(definition).model_dump()

    def update(self, task_id: str, definition: AgentLoopDefinition) -> dict:
        self._ensure_loaded()
        self._validate_id(task_id)
        if task_id != definition.id:
            raise HTTPException(status_code=400, detail="Loop task id cannot be changed")
        if task_id not in self._definitions:
            raise HTTPException(status_code=404, detail="Loop task not found")
        self._write_definition(definition)
        self._definitions[task_id] = definition
        runtime = self._state.setdefault(task_id, AgentLoopRuntime())
        runtime.next_run_at = self._compute_next_run(definition, runtime, time.time())
        self._write_state()
        logger.info("Updated agent loop task id={}", task_id)
        return self._info(definition).model_dump()

    def delete(self, task_id: str) -> dict[str, str]:
        self._ensure_loaded()
        if task_id not in self._definitions:
            raise HTTPException(status_code=404, detail="Loop task not found")
        self._path_for(task_id).unlink(missing_ok=True)
        self._definitions.pop(task_id, None)
        self._state.pop(task_id, None)
        self._write_state()
        logger.info("Deleted agent loop task id={}", task_id)
        return {"status": "deleted"}

    def reload(self) -> list[dict]:
        self._ensure_loaded()
        self._load_definitions()
        logger.info("Reloaded agent loop definitions count={}", len(self._definitions))
        return self.list()

    def pause(self, task_id: str, paused: bool) -> dict:
        definition = self._definition_or_404(task_id)
        runtime = self._state.setdefault(task_id, AgentLoopRuntime())
        runtime.paused = paused
        runtime.next_run_at = self._compute_next_run(definition, runtime, time.time())
        self._write_state()
        logger.info("{} agent loop task id={}", "Paused" if paused else "Resumed", task_id)
        return self._info(definition).model_dump()

    def reset_session(self, task_id: str) -> dict:
        definition = self._definition_or_404(task_id)
        runtime = self._state.setdefault(task_id, AgentLoopRuntime())
        runtime.current_session_id = None
        runtime.session_run_count = 0
        self._write_state()
        logger.info("Reset agent loop session id={}", task_id)
        return self._info(definition).model_dump()

    async def run_now(self, task_id: str, trigger: str = "manual") -> dict:
        definition = self._definition_or_404(task_id)
        runtime = self._state.setdefault(task_id, AgentLoopRuntime())
        async with self._lock:
            await self._complete_finished_runs()
            await self._start_run(definition, runtime, trigger)
            self._write_state()
        return self._info(definition).model_dump()

    def runs(self, task_id: str) -> list[dict]:
        self._definition_or_404(task_id)
        path = self._task_log_dir(task_id) / "runs.jsonl"
        if not path.exists():
            return []
        records_by_id: dict[str, dict] = {}
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
            try:
                item = json.loads(line)
            except json.JSONDecodeError:
                continue
            item.pop("prompt", None)
            item.pop("session_snapshot", None)
            run_id = item.get("run_id")
            if isinstance(run_id, str):
                records_by_id[run_id] = item
        return sorted(records_by_id.values(), key=lambda item: float(item.get("started_at") or 0), reverse=True)

    def run_detail(self, task_id: str, run_id: str) -> dict:
        self._definition_or_404(task_id)
        path = self._task_log_dir(task_id) / f"{run_id}.json"
        if not path.exists():
            raise HTTPException(status_code=404, detail="Loop run not found")
        return json.loads(path.read_text(encoding="utf-8"))

    def _definition_or_404(self, task_id: str) -> AgentLoopDefinition:
        self._ensure_loaded()
        definition = self._definitions.get(task_id)
        if not definition:
            raise HTTPException(status_code=404, detail="Loop task not found")
        return definition

    def _write_definition(self, definition: AgentLoopDefinition) -> None:
        ensure_view_home()
        self._path_for(definition.id).write_text(_definition_to_frontmatter(definition), encoding="utf-8")

    def _task_log_dir(self, task_id: str) -> Path:
        self._validate_id(task_id)
        path = AGENT_LOOP_LOG_DIR / task_id
        path.mkdir(parents=True, exist_ok=True)
        return path

    def _write_run(self, record: AgentLoopRunRecord) -> None:
        log_dir = self._task_log_dir(record.task_id)
        detail = log_dir / f"{record.run_id}.json"
        payload = record.model_dump()
        detail.write_text(json.dumps(payload, indent=2), encoding="utf-8")
        summary = dict(payload)
        summary.pop("session_snapshot", None)
        summary.pop("prompt", None)
        with (log_dir / "runs.jsonl").open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(summary) + "\n")

    async def _scheduler(self) -> None:
        assert self._stop_event is not None
        while not self._stop_event.is_set():
            try:
                async with self._lock:
                    await self._complete_finished_runs()
                    now = time.time()
                    for definition in list(self._definitions.values()):
                        runtime = self._state.setdefault(definition.id, AgentLoopRuntime())
                        if runtime.next_run_at is None:
                            runtime.next_run_at = self._compute_next_run(definition, runtime, now)
                        if runtime.next_run_at is not None and runtime.next_run_at <= now:
                            await self._start_run(definition, runtime, "schedule")
                    self._write_state()
            except Exception:
                logger.exception("Agent loop scheduler tick failed")
            try:
                await asyncio.wait_for(self._stop_event.wait(), timeout=5)
            except asyncio.TimeoutError:
                pass

    async def _start_run(self, definition: AgentLoopDefinition, runtime: AgentLoopRuntime, trigger: str) -> None:
        now = time.time()
        runtime.last_error = None
        if not definition.enabled or runtime.paused or runtime.stopped:
            runtime.next_run_at = self._compute_next_run(definition, runtime, now)
            return
        if runtime.current_run_id:
            if definition.run.skip_if_previous_running:
                logger.info("Skipping agent loop id={} because previous run is active", definition.id)
                runtime.next_run_at = self._compute_next_run(definition, runtime, now + 1)
                return
            raise HTTPException(status_code=409, detail="Previous loop run is still active")
        if definition.run.max_runs is not None and runtime.run_count >= definition.run.max_runs:
            runtime.stopped = True
            runtime.stop_reason = "max_runs"
            runtime.next_run_at = None
            return
        if definition.run.max_consecutive_failures is not None and runtime.consecutive_failures >= definition.run.max_consecutive_failures:
            runtime.stopped = True
            runtime.stop_reason = "max_consecutive_failures"
            runtime.next_run_at = None
            return
        session_id = await self._session_for_run(definition, runtime)
        run_id = f"{datetime.now(timezone.utc).strftime('%Y%m%d-%H%M%S')}-{uuid.uuid4().hex[:8]}"
        runtime.current_session_id = session_id
        runtime.current_run_id = run_id
        runtime.current_trigger = trigger
        runtime.run_count += 1
        runtime.session_run_count += 1
        runtime.last_run_at = now
        runtime.last_status = "running"
        runtime.next_run_at = self._compute_next_run(definition, runtime, now)
        record = AgentLoopRunRecord(
            run_id=run_id,
            task_id=definition.id,
            task_name=definition.name,
            codex_session_id=session_id,
            trigger=trigger,
            model=definition.model,
            cwd=definition.cwd,
            started_at=now,
            prompt=definition.prompt,
        )
        self._write_run(record)
        logger.info("Started agent loop id={} run={} session={} trigger={}", definition.id, run_id, session_id, trigger)

    async def _session_for_run(self, definition: AgentLoopDefinition, runtime: AgentLoopRuntime) -> str:
        if self._should_reset_session(definition, runtime):
            runtime.current_session_id = None
            runtime.session_run_count = 0
        session_id = runtime.current_session_id
        if session_id:
            try:
                summary = codex_session_manager.get(session_id).summary()
                if summary["status"] == "running":
                    raise HTTPException(status_code=409, detail="Codex session is already running")
                await codex_session_manager.send(session_id, definition.prompt, definition.model)
                return session_id
            except HTTPException:
                logger.info("Agent loop id={} cannot reuse session {}; creating a new session", definition.id, session_id)
        summary = await codex_session_manager.create(definition.prompt, definition.cwd, definition.model)
        return str(summary["id"])

    def _should_reset_session(self, definition: AgentLoopDefinition, runtime: AgentLoopRuntime) -> bool:
        policy = definition.session.policy
        if policy == "new_each_run":
            return True
        if not runtime.current_session_id:
            return False
        if policy in {"reuse_until_context", "reuse_with_limits"}:
            try:
                summary = codex_session_manager.get(runtime.current_session_id).summary()
                context = summary.get("context_used_percent")
                if isinstance(context, (int, float)) and context >= definition.session.max_context_percent:
                    return True
            except HTTPException:
                return True
        if policy == "reuse_with_limits":
            if definition.session.reset_after_runs and runtime.session_run_count >= definition.session.reset_after_runs:
                return True
            if definition.session.reset_on_failure and runtime.last_status == "failed":
                return True
        return False

    async def _complete_finished_runs(self) -> None:
        for definition in list(self._definitions.values()):
            runtime = self._state.setdefault(definition.id, AgentLoopRuntime())
            if not runtime.current_run_id or not runtime.current_session_id:
                continue
            try:
                snapshot = codex_session_manager.snapshot(runtime.current_session_id)
            except HTTPException as exc:
                self._finish_run(definition, runtime, "failed", None, str(exc), None)
                continue
            if snapshot.get("status") == "running":
                continue
            status = str(snapshot.get("status") or "failed")
            self._finish_run(definition, runtime, status, snapshot.get("exit_code"), None, snapshot)

    def _finish_run(
        self,
        definition: AgentLoopDefinition,
        runtime: AgentLoopRuntime,
        status: str,
        exit_code: int | None,
        error: str | None,
        snapshot: dict | None,
    ) -> None:
        run_id = runtime.current_run_id
        if not run_id:
            return
        finished_at = time.time()
        normalized_status = "failed" if status == "failed" or error else "exited"
        runtime.current_run_id = None
        trigger = runtime.current_trigger or "schedule"
        runtime.current_trigger = None
        runtime.last_status = normalized_status
        runtime.last_error = error
        runtime.consecutive_failures = runtime.consecutive_failures + 1 if normalized_status == "failed" else 0
        record = AgentLoopRunRecord(
            run_id=run_id,
            task_id=definition.id,
            task_name=definition.name,
            codex_session_id=runtime.current_session_id,
            trigger=trigger,
            model=definition.model,
            cwd=definition.cwd,
            started_at=runtime.last_run_at or finished_at,
            finished_at=finished_at,
            status=normalized_status,
            exit_code=exit_code,
            error=error,
            prompt=definition.prompt,
            session_snapshot=snapshot,
        )
        self._write_run(record)
        if self._stop_matched(definition, snapshot):
            runtime.stopped = True
            runtime.stop_reason = "final_message_regex"
            runtime.next_run_at = None
        else:
            runtime.next_run_at = self._compute_next_run(definition, runtime, finished_at)
        logger.info("Finished agent loop id={} run={} status={}", definition.id, run_id, normalized_status)

    def _stop_matched(self, definition: AgentLoopDefinition, snapshot: dict | None) -> bool:
        pattern = definition.stop.final_message_regex
        if not pattern or not snapshot:
            return False
        text = "\n".join(str(event.get("text") or "") for event in snapshot.get("events") or [])
        try:
            return re.search(pattern, text) is not None
        except re.error:
            logger.warning("Invalid stop regex for agent loop id={}", definition.id)
            return False

    def _compute_next_run(self, definition: AgentLoopDefinition, runtime: AgentLoopRuntime, now: float) -> float | None:
        if not definition.enabled or runtime.paused or runtime.stopped:
            return None
        schedule = definition.schedule
        zone = _local_zone(definition.timezone)
        if schedule.type == "manual":
            return None
        if schedule.type == "once":
            if runtime.run_count > 0:
                return None
            at = _timestamp_from_local(schedule.at_local, zone)
            return at if at and at > now else now
        if schedule.type == "interval":
            start_at = _timestamp_from_local(schedule.start_at_local, zone)
            if runtime.last_run_at is None:
                return start_at if start_at and start_at > now else now
            return max(runtime.last_run_at + schedule.every_minutes * 60, now + 1)
        if schedule.type == "daily":
            return _next_daily(now, zone, [schedule.time_local])
        if schedule.type == "multi_daily":
            return _next_daily(now, zone, schedule.times_local)
        return None


agent_loop_manager = AgentLoopManager()
