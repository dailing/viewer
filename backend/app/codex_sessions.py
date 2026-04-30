import asyncio
from collections import deque
import calendar
from contextlib import suppress
import json
import os
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from fastapi import HTTPException, WebSocket, WebSocketDisconnect
from loguru import logger
from watchfiles import awatch

from .config import settings
from .files import resolve_served_directory
from .ws_clients import WebSocketClient, add_client, broadcast, enqueue, remove_client

CODEX_LOG_DIR = Path(__file__).resolve().parents[2] / "logs" / "codex-sessions"
CODEX_ROLLOUT_ROOT = Path.home() / ".codex" / "sessions"
CODEX_PROXY = "http://localhost:7890"


@dataclass
class CodexSession:
    id: str
    title: str
    cwd: str
    model: str | None
    created_at: float
    updated_at: float
    codex_session_id: str | None = None
    status: str = "idle"
    exit_code: int | None = None
    prompts: list[dict] = field(default_factory=list)
    events: list[dict] = field(default_factory=list)
    clients: dict[WebSocket, WebSocketClient] = field(default_factory=dict)
    run_task: asyncio.Task | None = None
    process: asyncio.subprocess.Process | None = None
    meta_path: Path | None = None
    log_path: Path | None = None
    stderr_path: Path | None = None
    rollout_path: Path | None = None

    def summary(self) -> dict:
        return {
            "id": self.id,
            "codex_session_id": self.codex_session_id,
            "rollout_path": self.rollout_path.as_posix() if self.rollout_path else None,
            "title": self.title,
            "cwd": self.cwd,
            "model": self.model,
            "created_at": self.created_at,
            "updated_at": self.updated_at,
            "status": self.status,
            "exit_code": self.exit_code,
            "event_count": len(self.events),
        }

    def snapshot(self) -> dict:
        return {**self.summary(), "prompts": self.prompts, "events": self.events}


class CodexSessionManager:
    def __init__(self) -> None:
        self.sessions: dict[str, CodexSession] = {}
        self._loaded = False
        self._status_cache: dict[str, Any] | None = None
        self._status_cache_key: tuple[str, int, int] | None = None

    def _paths(self, session_id: str) -> tuple[Path, Path, Path]:
        return (
            CODEX_LOG_DIR / f"{session_id}.json",
            CODEX_LOG_DIR / f"{session_id}.jsonl",
            CODEX_LOG_DIR / f"{session_id}.stderr.log",
        )

    def _cwd_for(self, cwd: str | None) -> str:
        return resolve_served_directory(cwd, "Codex")

    def _relative_cwd(self, cwd: str) -> str:
        path = Path(cwd)
        try:
            return path.relative_to(settings.root_resolved).as_posix()
        except ValueError:
            return path.as_posix()

    def _ensure_loaded(self) -> None:
        if self._loaded:
            return
        CODEX_LOG_DIR.mkdir(parents=True, exist_ok=True)
        for meta_path in sorted(CODEX_LOG_DIR.glob("*.json")):
            try:
                meta = json.loads(meta_path.read_text(encoding="utf-8"))
                session_id = str(meta["id"])
                _, log_path, stderr_path = self._paths(session_id)
                rollout_path = self._rollout_path_from_meta(meta.get("rollout_path"))
                status = meta.get("status", "exited")
                if status == "running":
                    status = "failed"
                session = CodexSession(
                    id=session_id,
                    codex_session_id=meta.get("codex_session_id"),
                    title=meta.get("title") or "Codex session",
                    cwd=meta.get("cwd") or settings.root_resolved.as_posix(),
                    model=meta.get("model"),
                    created_at=float(meta.get("created_at") or time.time()),
                    updated_at=float(meta.get("updated_at") or time.time()),
                    status=status,
                    exit_code=meta.get("exit_code"),
                    prompts=list(meta.get("prompts") or []),
                    meta_path=meta_path,
                    log_path=log_path,
                    stderr_path=stderr_path,
                    rollout_path=rollout_path,
                )
                self._sync_rollout_events(session)
                self.sessions[session_id] = session
            except Exception:
                logger.warning("Failed to load Codex session metadata {}", meta_path)
        self._loaded = True

    def _write_meta(self, session: CodexSession) -> None:
        if session.meta_path is None:
            return
        session.meta_path.parent.mkdir(parents=True, exist_ok=True)
        session.meta_path.write_text(
            json.dumps(
                {
                    "id": session.id,
                    "codex_session_id": session.codex_session_id,
                    "rollout_path": session.rollout_path.as_posix() if session.rollout_path else None,
                    "title": session.title,
                    "cwd": session.cwd,
                    "model": session.model,
                    "created_at": session.created_at,
                    "updated_at": session.updated_at,
                    "status": session.status,
                    "exit_code": session.exit_code,
                    "prompts": session.prompts,
                },
                indent=2,
            ),
            encoding="utf-8",
        )

    def _append_stderr(self, session: CodexSession, line: str) -> None:
        if session.stderr_path is None:
            return
        session.stderr_path.parent.mkdir(parents=True, exist_ok=True)
        with session.stderr_path.open("a", encoding="utf-8") as handle:
            handle.write(line)

    def _find_session_id(self, value: Any) -> str | None:
        if isinstance(value, dict):
            for key in ("session_id", "conversation_id", "thread_id"):
                found = value.get(key)
                if isinstance(found, str) and found:
                    return found
            for item in value.values():
                found = self._find_session_id(item)
                if found:
                    return found
        elif isinstance(value, list):
            for item in value:
                found = self._find_session_id(item)
                if found:
                    return found
        return None

    def _rollout_path_from_meta(self, value: Any) -> Path | None:
        if not isinstance(value, str) or not value:
            return None
        path = Path(value).expanduser()
        try:
            if path.exists() and path.is_file():
                return path
        except OSError:
            return None
        return None

    def _timestamp_value(self, raw: dict) -> float:
        timestamp = raw.get("timestamp")
        if isinstance(timestamp, str):
            for pattern in ("%Y-%m-%dT%H:%M:%S.%fZ", "%Y-%m-%dT%H:%M:%SZ"):
                try:
                    return float(calendar.timegm(time.strptime(timestamp, pattern)))
                except ValueError:
                    pass
        return time.time()

    def _rollout_session_id(self, path: Path) -> str | None:
        try:
            with path.open("r", encoding="utf-8", errors="replace") as handle:
                for line in handle:
                    if not line.strip():
                        continue
                    try:
                        raw = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    payload = raw.get("payload")
                    if raw.get("type") == "session_meta" and isinstance(payload, dict):
                        session_id = payload.get("id")
                        return session_id if isinstance(session_id, str) and session_id else None
        except OSError:
            return None
        return None

    def _find_rollout_for_session(self, codex_session_id: str | None) -> Path | None:
        if not codex_session_id or not CODEX_ROLLOUT_ROOT.exists():
            return None
        try:
            paths = sorted(CODEX_ROLLOUT_ROOT.rglob("rollout-*.jsonl"), key=lambda item: item.stat().st_mtime, reverse=True)
        except OSError:
            return None
        for path in paths:
            if self._rollout_session_id(path) == codex_session_id:
                return path
        return None

    def _raw_type(self, raw: dict) -> str:
        payload = raw.get("payload")
        if raw.get("type") in ("event_msg", "response_item") and isinstance(payload, dict):
            payload_type = payload.get("type")
            if isinstance(payload_type, str):
                return payload_type
        raw_type = raw.get("type")
        return raw_type if isinstance(raw_type, str) else "event"

    def _turn_finished_status(self, events: list[dict]) -> str | None:
        status: str | None = None
        for event in events:
            raw = event.get("raw")
            if not isinstance(raw, dict):
                continue
            event_type = self._raw_type(raw)
            if event_type in ("task_complete", "turn.completed"):
                status = "exited"
            elif event_type in ("turn_aborted", "turn.failed"):
                status = "failed"
        return status

    def _sync_rollout_events(self, session: CodexSession) -> list[dict]:
        if session.rollout_path is None:
            session.rollout_path = self._find_rollout_for_session(session.codex_session_id)
            if session.rollout_path is not None:
                logger.info("Codex session {} matched rollout file {}", session.id, session.rollout_path)
                self._write_meta(session)
        if session.rollout_path is None or not session.rollout_path.exists():
            logger.debug("Codex session {} rollout file unavailable codex_session_id={}", session.id, session.codex_session_id)
            return []

        events: list[dict] = []
        try:
            with session.rollout_path.open("r", encoding="utf-8", errors="replace") as handle:
                for line in handle:
                    raw_line = line.rstrip("\n")
                    if not raw_line.strip():
                        continue
                    try:
                        raw = json.loads(raw_line)
                    except json.JSONDecodeError:
                        raw = {"type": "invalid_json", "line": raw_line}
                    events.append({"index": len(events), "received_at": self._timestamp_value(raw), "raw": raw})
        except OSError:
            return []

        old_count = len(session.events)
        session.events = events
        new_events = events[old_count:]
        turn_status = self._turn_finished_status(new_events)
        if session.status == "running" and turn_status is not None:
            session.status = turn_status
            if turn_status == "exited" and session.exit_code is None:
                session.exit_code = 0
            logger.info("Codex session {} marked {} from rollout turn-finish event", session.id, turn_status)
        if events:
            session.updated_at = max(session.updated_at, float(events[-1]["received_at"]))
        if new_events:
            logger.debug(
                "Codex session {} synced {} new rollout event(s) total={} status={} clients={} path={}",
                session.id,
                len(new_events),
                len(events),
                session.status,
                len(session.clients),
                session.rollout_path,
            )
        self._write_meta(session)
        return new_events

    def _title_for(self, prompt: str) -> str:
        first = " ".join(prompt.strip().split())
        if not first:
            return "Codex session"
        return first[:72]

    def list(self) -> list[dict]:
        self._ensure_loaded()
        for session in self.sessions.values():
            if session.status == "running" and session.clients:
                continue
            self._sync_rollout_events(session)
        return sorted((session.summary() for session in self.sessions.values()), key=lambda item: item["updated_at"], reverse=True)

    def get(self, session_id: str) -> CodexSession:
        self._ensure_loaded()
        session = self.sessions.get(session_id)
        if not session:
            raise HTTPException(status_code=404, detail="Codex session not found")
        self._sync_rollout_events(session)
        return session

    async def create(self, prompt: str, cwd: str | None = None, model: str | None = None) -> dict:
        self._ensure_loaded()
        session_id = uuid.uuid4().hex
        meta_path, log_path, stderr_path = self._paths(session_id)
        now = time.time()
        cleaned_prompt = prompt.strip()
        session = CodexSession(
            id=session_id,
            title=self._title_for(cleaned_prompt) if cleaned_prompt else "New Codex session",
            cwd=self._cwd_for(cwd),
            model=model.strip() if isinstance(model, str) and model.strip() else None,
            created_at=now,
            updated_at=now,
            status="running" if cleaned_prompt else "idle",
            prompts=[{"text": cleaned_prompt, "created_at": now}] if cleaned_prompt else [],
            meta_path=meta_path,
            log_path=log_path,
            stderr_path=stderr_path,
        )
        self.sessions[session_id] = session
        stderr_path.write_text("", encoding="utf-8")
        self._write_meta(session)
        if cleaned_prompt:
            session.run_task = asyncio.create_task(self._run(session, cleaned_prompt, resume=False))
        return session.summary()

    async def send(self, session_id: str, prompt: str, model: str | None = None) -> dict:
        session = self.get(session_id)
        if not prompt.strip():
            raise HTTPException(status_code=400, detail="Prompt is required")
        if session.status == "running":
            raise HTTPException(status_code=409, detail="Codex session is already running")
        resume = bool(session.codex_session_id)
        now = time.time()
        session.prompts.append({"text": prompt, "created_at": now})
        if model:
            session.model = model
        if not session.codex_session_id:
            session.title = self._title_for(prompt)
        session.status = "running"
        session.exit_code = None
        session.updated_at = now
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary()})
        session.run_task = asyncio.create_task(self._run(session, prompt, resume=resume))
        return session.summary()

    async def delete(self, session_id: str) -> dict[str, str]:
        session = self.get(session_id)
        if session.process and session.process.returncode is None:
            session.process.terminate()
            try:
                await asyncio.wait_for(session.process.wait(), timeout=1.5)
            except asyncio.TimeoutError:
                session.process.kill()
                await session.process.wait()
        for path in (session.meta_path, session.log_path, session.stderr_path):
            if path:
                path.unlink(missing_ok=True)
        self.sessions.pop(session_id, None)
        await self._broadcast(session, {"type": "deleted"})
        return {"status": "deleted"}

    async def terminate(self, session_id: str) -> dict:
        session = self.get(session_id)
        if session.process and session.process.returncode is None:
            session.process.terminate()
            try:
                await asyncio.wait_for(session.process.wait(), timeout=1.5)
            except asyncio.TimeoutError:
                session.process.kill()
                await session.process.wait()
            session.exit_code = session.process.returncode
        session.process = None
        if session.status == "running":
            session.status = "exited"
        session.updated_at = time.time()
        self._write_meta(session)
        await self._broadcast(session, {"type": "status", "session": session.summary()})
        return session.summary()

    async def connect(self, session_id: str, websocket: WebSocket) -> None:
        session = self.get(session_id)
        await websocket.accept()
        client = add_client(session.clients, websocket)
        enqueue(client, {"type": "snapshot", "session": session.snapshot()})
        try:
            while True:
                await websocket.receive_text()
        except WebSocketDisconnect:
            pass
        except RuntimeError as exc:
            if "disconnect" not in str(exc).lower():
                raise
        finally:
            await self._remove_client(session, client)

    async def shutdown(self) -> None:
        for session in list(self.sessions.values()):
            if session.process and session.process.returncode is None:
                session.process.terminate()

    def cli_status(self) -> dict:
        latest = self._latest_rollout()
        if latest is None:
            return {"available": False}
        cache_key = (latest.as_posix(), int(latest.stat().st_mtime), latest.stat().st_size)
        if self._status_cache is not None and self._status_cache_key == cache_key:
            return self._status_cache
        status = self._parse_rollout_status(latest)
        self._status_cache = status
        self._status_cache_key = cache_key
        return status

    def _latest_rollout(self) -> Path | None:
        if not CODEX_ROLLOUT_ROOT.exists():
            return None
        latest_path: Path | None = None
        latest_mtime = -1.0
        for path in CODEX_ROLLOUT_ROOT.rglob("rollout-*.jsonl"):
            try:
                mtime = path.stat().st_mtime
            except OSError:
                continue
            if mtime > latest_mtime:
                latest_path = path
                latest_mtime = mtime
        return latest_path

    def _tail_lines(self, path: Path, max_lines: int = 1800) -> list[str]:
        lines: deque[str] = deque(maxlen=max_lines)
        with path.open("r", encoding="utf-8", errors="replace") as handle:
            for line in handle:
                lines.append(line)
        return list(lines)

    def _parse_rollout_status(self, path: Path) -> dict:
        session_id: str | None = None
        updated_at: float | None = None
        cwd: str | None = None
        model: str | None = None
        context_window: int | None = None
        total_tokens: int | None = None
        context_used_percent: float | None = None
        plan_type: str | None = None
        primary_used_percent: float | None = None
        primary_window_minutes: int | None = None
        secondary_used_percent: float | None = None
        secondary_window_minutes: int | None = None

        for line in self._tail_lines(path):
            raw = line.strip()
            if not raw:
                continue
            try:
                event = json.loads(raw)
            except json.JSONDecodeError:
                continue
            event_type = event.get("type")
            payload = event.get("payload")
            if not isinstance(payload, dict):
                continue
            if event_type == "session_meta":
                sid = payload.get("id")
                if isinstance(sid, str) and sid:
                    session_id = sid
                cwd_value = payload.get("cwd")
                if isinstance(cwd_value, str) and cwd_value:
                    cwd = cwd_value
            elif event_type == "turn_context":
                model_value = payload.get("model")
                if isinstance(model_value, str) and model_value:
                    model = model_value
                cwd_value = payload.get("cwd")
                if isinstance(cwd_value, str) and cwd_value:
                    cwd = cwd_value
            elif event_type == "event_msg":
                msg_type = payload.get("type")
                if msg_type != "token_count":
                    continue
                info = payload.get("info")
                if isinstance(info, dict):
                    context = info.get("model_context_window")
                    if isinstance(context, int) and context > 0:
                        context_window = context
                    usage = info.get("total_token_usage")
                    if isinstance(usage, dict):
                        total = usage.get("total_tokens")
                        if isinstance(total, int):
                            total_tokens = total
                            if context_window and context_window > 0:
                                context_used_percent = round((total / context_window) * 100, 1)
                rate_limits = payload.get("rate_limits")
                if isinstance(rate_limits, dict):
                    plan = rate_limits.get("plan_type")
                    if isinstance(plan, str):
                        plan_type = plan
                    primary = rate_limits.get("primary")
                    if isinstance(primary, dict):
                        used = primary.get("used_percent")
                        if isinstance(used, (int, float)):
                            value = float(used)
                            primary_used_percent = round(value * 100, 1) if value <= 1 else round(value, 1)
                        window = primary.get("window_minutes")
                        if isinstance(window, int):
                            primary_window_minutes = window
                    secondary = rate_limits.get("secondary")
                    if isinstance(secondary, dict):
                        used = secondary.get("used_percent")
                        if isinstance(used, (int, float)):
                            value = float(used)
                            secondary_used_percent = round(value * 100, 1) if value <= 1 else round(value, 1)
                        window = secondary.get("window_minutes")
                        if isinstance(window, int):
                            secondary_window_minutes = window
                timestamp = event.get("timestamp")
                if isinstance(timestamp, str):
                    try:
                        updated_at = calendar.timegm(time.strptime(timestamp, "%Y-%m-%dT%H:%M:%S.%fZ"))
                    except ValueError:
                        pass

        return {
            "available": True,
            "session_id": session_id,
            "rollout_path": path.as_posix(),
            "updated_at": updated_at,
            "cwd": cwd,
            "model": model,
            "model_context_window": context_window,
            "context_used_percent": context_used_percent,
            "total_tokens": total_tokens,
            "plan_type": plan_type,
            "primary_used_percent": primary_used_percent,
            "primary_window_minutes": primary_window_minutes,
            "secondary_used_percent": secondary_used_percent,
            "secondary_window_minutes": secondary_window_minutes,
            "selected_model": model,
        }

    async def model_options(self) -> dict:
        from .files import read_config

        config = read_config().codex
        selected = config.default_model
        available = config.available_models or [selected]
        if selected not in available:
            available = [selected, *available]
        return {"selected_model": selected, "available_models": available, "source": "config"}

    async def _run(self, session: CodexSession, prompt: str, *, resume: bool) -> None:
        command = ["codex", "exec"]
        if resume:
            assert session.codex_session_id is not None
            command.extend([
                "resume",
                "--json",
                "--skip-git-repo-check",
                "--dangerously-bypass-approvals-and-sandbox",
                session.codex_session_id,
            ])
        else:
            command.extend([
                "--json",
                "--skip-git-repo-check",
                "--dangerously-bypass-approvals-and-sandbox",
                "-C",
                session.cwd,
            ])
        if session.model:
            command.extend(["-m", session.model])
        command.append("-")
        logger.info("Starting Codex session {} resume={} cwd={}", session.id, resume, session.cwd)
        try:
            process = await asyncio.create_subprocess_exec(
                *command,
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                cwd=session.cwd,
                env={
                    **os.environ,
                    "https_proxy": CODEX_PROXY,
                    "HTTPS_PROXY": CODEX_PROXY,
                    "http_proxy": CODEX_PROXY,
                    "HTTP_PROXY": CODEX_PROXY,
                },
            )
        except FileNotFoundError:
            session.status = "failed"
            session.exit_code = 127
            session.updated_at = time.time()
            self._write_meta(session)
            await self._broadcast(session, {"type": "status", "session": session.summary()})
            return

        session.process = process
        assert process.stdin is not None
        process.stdin.write(prompt.encode())
        await process.stdin.drain()
        process.stdin.close()
        watch_stop = asyncio.Event()
        watch_task = asyncio.create_task(self._watch_rollout_events(session, process, watch_stop))
        try:
            await asyncio.gather(self._read_stdout(session, process), self._read_stderr(session, process))
        finally:
            watch_stop.set()
            watch_task.cancel()
            with suppress(asyncio.CancelledError):
                await watch_task
        exit_code = await process.wait()
        await self._sync_and_broadcast_rollout_events(session)
        session.exit_code = exit_code
        session.status = "exited" if exit_code == 0 else "failed"
        session.updated_at = time.time()
        session.process = None
        self._write_meta(session)
        logger.info("Codex session {} exited code={}", session.id, exit_code)
        await self._broadcast(session, {"type": "status", "session": session.summary()})

    async def _read_stdout(self, session: CodexSession, process: asyncio.subprocess.Process) -> None:
        assert process.stdout is not None
        while True:
            line = await process.stdout.readline()
            if not line:
                return
            text = line.decode(encoding="utf-8", errors="replace")
            try:
                raw = json.loads(text)
            except json.JSONDecodeError:
                raw = {}
            codex_session_id = self._find_session_id(raw)
            if codex_session_id and codex_session_id != session.codex_session_id:
                session.codex_session_id = codex_session_id
                session.rollout_path = self._find_rollout_for_session(codex_session_id)
                logger.info(
                    "Codex session {} discovered codex_session_id={} rollout_path={}",
                    session.id,
                    codex_session_id,
                    session.rollout_path,
                )
                self._write_meta(session)
            await self._sync_and_broadcast_rollout_events(session)

    async def _watch_rollout_events(self, session: CodexSession, process: asyncio.subprocess.Process, stop_event: asyncio.Event) -> None:
        while process.returncode is None and not stop_event.is_set():
            if session.rollout_path is None:
                if session.codex_session_id:
                    session.rollout_path = self._find_rollout_for_session(session.codex_session_id)
                    if session.rollout_path is not None:
                        logger.info("Codex session {} rollout watcher matched {}", session.id, session.rollout_path)
                        self._write_meta(session)
                await self._sync_and_broadcast_rollout_events(session)
                await asyncio.sleep(0.25)
                continue

            path = session.rollout_path
            parent = path.parent
            logger.debug("Codex session {} watching rollout file {}", session.id, path)
            await self._sync_and_broadcast_rollout_events(session)
            if not parent.exists():
                logger.debug("Codex session {} rollout parent missing {}", session.id, parent)
                await asyncio.sleep(0.5)
                continue

            try:
                async for changes in awatch(
                    parent,
                    stop_event=stop_event,
                    watch_filter=lambda _change, raw_path: Path(raw_path) == path,
                    debounce=100,
                    debug=False,
                ):
                    if process.returncode is not None or stop_event.is_set() or session.rollout_path != path:
                        break
                    if any(Path(raw_path) == path for _change, raw_path in changes):
                        logger.debug("Codex session {} detected rollout file change {}", session.id, path)
                        await self._sync_and_broadcast_rollout_events(session)
            except OSError:
                logger.debug("Codex session {} rollout watcher failed for {}; retrying", session.id, path)
                await asyncio.sleep(0.5)

    async def _sync_and_broadcast_rollout_events(self, session: CodexSession) -> None:
        for event in self._sync_rollout_events(session):
            raw = event.get("raw")
            event_type = self._raw_type(raw) if isinstance(raw, dict) else "event"
            logger.debug(
                "Codex session {} broadcasting rollout event index={} type={} status={} clients={}",
                session.id,
                event.get("index"),
                event_type,
                session.status,
                len(session.clients),
            )
            await self._broadcast(session, {"type": "event", "event": event, "session": session.summary()})

    async def _read_stderr(self, session: CodexSession, process: asyncio.subprocess.Process) -> None:
        assert process.stderr is not None
        while True:
            line = await process.stderr.readline()
            if not line:
                return
            text = line.decode(encoding="utf-8", errors="replace")
            self._append_stderr(session, text)
            logger.debug("Codex session {} stderr: {}", session.id, text.rstrip())

    async def _broadcast(self, session: CodexSession, message: dict) -> None:
        stale = await broadcast(session.clients, message)
        logger.debug(
            "Codex session {} websocket broadcast type={} clients={} stale_removed={}",
            session.id,
            message.get("type"),
            len(session.clients),
            len(stale),
        )

    async def _remove_client(self, session: CodexSession, client: WebSocketClient) -> None:
        await remove_client(session.clients, client)


codex_session_manager = CodexSessionManager()
