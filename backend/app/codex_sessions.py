import asyncio
import json
import os
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from fastapi import HTTPException, WebSocket, WebSocketDisconnect
from loguru import logger

from .config import settings
from .files import resolve_served_directory
from .ws_clients import WebSocketClient, add_client, broadcast, enqueue, remove_client

CODEX_LOG_DIR = Path(__file__).resolve().parents[2] / "logs" / "codex-sessions"
CODEX_PROXY = "http://localhost:7890"


@dataclass
class CodexSession:
    id: str
    title: str
    cwd: str
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

    def summary(self) -> dict:
        return {
            "id": self.id,
            "codex_session_id": self.codex_session_id,
            "title": self.title,
            "cwd": self.cwd,
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
                events = self._read_events(log_path)
                status = meta.get("status", "exited")
                if status == "running":
                    status = "failed"
                self.sessions[session_id] = CodexSession(
                    id=session_id,
                    codex_session_id=meta.get("codex_session_id"),
                    title=meta.get("title") or "Codex session",
                    cwd=meta.get("cwd") or settings.root_resolved.as_posix(),
                    created_at=float(meta.get("created_at") or time.time()),
                    updated_at=float(meta.get("updated_at") or time.time()),
                    status=status,
                    exit_code=meta.get("exit_code"),
                    prompts=list(meta.get("prompts") or []),
                    events=events,
                    meta_path=meta_path,
                    log_path=log_path,
                    stderr_path=stderr_path,
                )
            except Exception:
                logger.warning("Failed to load Codex session metadata {}", meta_path)
        self._loaded = True

    def _read_events(self, log_path: Path) -> list[dict]:
        if not log_path.exists():
            return []
        events: list[dict] = []
        for line in log_path.read_text(encoding="utf-8", errors="replace").splitlines():
            if not line.strip():
                continue
            try:
                raw = json.loads(line)
            except json.JSONDecodeError:
                raw = {"type": "invalid_json", "line": line}
            events.append({"index": len(events), "received_at": time.time(), "raw": raw})
        return events

    def _write_meta(self, session: CodexSession) -> None:
        if session.meta_path is None:
            return
        session.meta_path.parent.mkdir(parents=True, exist_ok=True)
        session.meta_path.write_text(
            json.dumps(
                {
                    "id": session.id,
                    "codex_session_id": session.codex_session_id,
                    "title": session.title,
                    "cwd": session.cwd,
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

    def _append_event(self, session: CodexSession, raw: dict) -> dict:
        assert session.log_path is not None
        session.log_path.parent.mkdir(parents=True, exist_ok=True)
        with session.log_path.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(raw, ensure_ascii=False) + "\n")
        event = {"index": len(session.events), "received_at": time.time(), "raw": raw}
        session.events.append(event)
        session.updated_at = event["received_at"]
        codex_session_id = self._find_session_id(raw)
        if codex_session_id and codex_session_id != session.codex_session_id:
            session.codex_session_id = codex_session_id
        self._write_meta(session)
        return event

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

    def _title_for(self, prompt: str) -> str:
        first = " ".join(prompt.strip().split())
        if not first:
            return "Codex session"
        return first[:72]

    def list(self) -> list[dict]:
        self._ensure_loaded()
        return sorted((session.summary() for session in self.sessions.values()), key=lambda item: item["updated_at"], reverse=True)

    def get(self, session_id: str) -> CodexSession:
        self._ensure_loaded()
        session = self.sessions.get(session_id)
        if not session:
            raise HTTPException(status_code=404, detail="Codex session not found")
        return session

    async def create(self, prompt: str, cwd: str | None = None) -> dict:
        self._ensure_loaded()
        session_id = uuid.uuid4().hex
        meta_path, log_path, stderr_path = self._paths(session_id)
        now = time.time()
        cleaned_prompt = prompt.strip()
        session = CodexSession(
            id=session_id,
            title=self._title_for(cleaned_prompt) if cleaned_prompt else "New Codex session",
            cwd=self._cwd_for(cwd),
            created_at=now,
            updated_at=now,
            status="running" if cleaned_prompt else "idle",
            prompts=[{"text": cleaned_prompt, "created_at": now}] if cleaned_prompt else [],
            meta_path=meta_path,
            log_path=log_path,
            stderr_path=stderr_path,
        )
        self.sessions[session_id] = session
        log_path.write_text("", encoding="utf-8")
        stderr_path.write_text("", encoding="utf-8")
        self._write_meta(session)
        if cleaned_prompt:
            session.run_task = asyncio.create_task(self._run(session, cleaned_prompt, resume=False))
        return session.summary()

    async def send(self, session_id: str, prompt: str) -> dict:
        session = self.get(session_id)
        if not prompt.strip():
            raise HTTPException(status_code=400, detail="Prompt is required")
        if session.status == "running":
            raise HTTPException(status_code=409, detail="Codex session is already running")
        resume = bool(session.codex_session_id)
        now = time.time()
        session.prompts.append({"text": prompt, "created_at": now})
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
                "-",
            ])
        else:
            command.extend([
                "--json",
                "--skip-git-repo-check",
                "--dangerously-bypass-approvals-and-sandbox",
                "-C",
                session.cwd,
                "-",
            ])
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
        await asyncio.gather(self._read_stdout(session, process), self._read_stderr(session, process))
        exit_code = await process.wait()
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
                raw = {"type": "invalid_json", "line": text.rstrip("\n")}
            event = self._append_event(session, raw)
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
        await broadcast(session.clients, message)

    async def _remove_client(self, session: CodexSession, client: WebSocketClient) -> None:
        await remove_client(session.clients, client)


codex_session_manager = CodexSessionManager()
