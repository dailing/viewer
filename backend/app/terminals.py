import asyncio
import fcntl
import os
import signal
import struct
import subprocess
import termios
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path

from fastapi import HTTPException, WebSocket, WebSocketDisconnect
from loguru import logger

from .config import settings
from .files import resolve_served_directory
from .storage import TERMINAL_LOG_DIR
from .ws_clients import WebSocketClient, add_client, broadcast, enqueue, remove_client

MAX_OUTPUT_CHARS = 1_000_000


@dataclass
class TerminalSession:
    id: str
    title: str
    shell: str
    cwd: str
    created_at: float
    output: str = ""
    output_version: int = 0
    status: str = "running"
    exit_code: int | None = None
    rows: int = 30
    cols: int = 120
    layout_locked: bool = False
    master_fd: int | None = None
    process: subprocess.Popen[bytes] | None = None
    clients: dict[WebSocket, WebSocketClient] = field(default_factory=dict)
    write_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    reader_task: asyncio.Task | None = None
    wait_task: asyncio.Task | None = None
    log_path: Path | None = None

    def snapshot(self) -> dict:
        return {
            "id": self.id,
            "title": self.title,
            "shell": self.shell,
            "cwd": self.cwd,
            "created_at": self.created_at,
            "status": self.status,
            "exit_code": self.exit_code,
            "rows": self.rows,
            "cols": self.cols,
            "layout_locked": self.layout_locked,
            "output": self.output,
            "output_version": self.output_version,
        }

    def summary(self) -> dict:
        return {
            "id": self.id,
            "title": self.title,
            "shell": self.shell,
            "cwd": self.cwd,
            "created_at": self.created_at,
            "status": self.status,
            "exit_code": self.exit_code,
            "rows": self.rows,
            "cols": self.cols,
            "layout_locked": self.layout_locked,
        }


class TerminalManager:
    def __init__(self) -> None:
        self.sessions: dict[str, TerminalSession] = {}
        self._counter = 0

    def list(self) -> list[dict]:
        return sorted((session.summary() for session in self.sessions.values()), key=lambda item: item["created_at"])

    def get(self, terminal_id: str) -> TerminalSession:
        session = self.sessions.get(terminal_id)
        if not session:
            raise HTTPException(status_code=404, detail="Terminal not found")
        return session

    def _cwd_for(self, cwd: str | None) -> str:
        return resolve_served_directory(cwd, "Terminal")

    async def create(self, cwd: str | None = None) -> dict:
        self._counter += 1
        terminal_id = uuid.uuid4().hex
        shell = settings.terminal_shell
        session = TerminalSession(
            id=terminal_id,
            title=f"Terminal {self._counter}",
            shell=shell,
            cwd=self._cwd_for(cwd),
            created_at=time.time(),
            log_path=TERMINAL_LOG_DIR / f"{terminal_id}.log",
        )
        master_fd, slave_fd = os.openpty()
        try:
            self._set_size(master_fd, 30, 120)
            def configure_child_pty() -> None:
                os.setsid()
                fcntl.ioctl(slave_fd, termios.TIOCSCTTY, 0)

            process = subprocess.Popen(
                [shell],
                cwd=session.cwd,
                env={
                    **os.environ,
                    "TERM": "xterm-256color",
                    "COLORTERM": "truecolor",
                },
                stdin=slave_fd,
                stdout=slave_fd,
                stderr=slave_fd,
                preexec_fn=configure_child_pty,
            )
        except FileNotFoundError as exc:
            os.close(master_fd)
            os.close(slave_fd)
            raise HTTPException(status_code=500, detail=f"Shell not found: {shell}") from exc
        finally:
            try:
                os.close(slave_fd)
            except OSError:
                pass

        os.set_blocking(master_fd, False)
        session.master_fd = master_fd
        session.process = process
        self.sessions[terminal_id] = session
        self._initialize_log(session)
        logger.info("Terminal {} started shell={} cwd={} pid={}", terminal_id, shell, session.cwd, process.pid)
        session.reader_task = asyncio.create_task(self._read_output(session))
        session.wait_task = asyncio.create_task(self._wait_for_exit(session))
        return session.summary()

    async def terminate(self, terminal_id: str) -> dict:
        session = self.get(terminal_id)
        await self._terminate_process(session)
        return session.summary()

    async def delete(self, terminal_id: str) -> dict[str, str]:
        session = self.get(terminal_id)
        await self._terminate_process(session)
        if session.reader_task:
            session.reader_task.cancel()
        if session.wait_task:
            session.wait_task.cancel()
        if session.master_fd is not None:
            try:
                os.close(session.master_fd)
            except OSError:
                pass
            session.master_fd = None
        self.sessions.pop(terminal_id, None)
        self._remove_log(session)
        await self._broadcast(session, {"type": "deleted"})
        return {"status": "deleted"}

    async def connect(self, terminal_id: str, websocket: WebSocket) -> None:
        session = self.get(terminal_id)
        await websocket.accept()
        client = add_client(session.clients, websocket)
        enqueue(client, {"type": "snapshot", "terminal": self._snapshot_for_client(session)})
        logger.info("Terminal {} websocket connected clients={}", terminal_id, len(session.clients))
        try:
            while True:
                message = await websocket.receive_json()
                if message.get("type") == "input":
                    await self.write(session, str(message.get("data", "")))
                elif message.get("type") == "resize":
                    rows = int(message.get("rows", 30))
                    cols = int(message.get("cols", 120))
                    lock = bool(message.get("lock", False))
                    await self.resize(session, rows, cols, lock=lock)
        except WebSocketDisconnect:
            pass
        except RuntimeError as exc:
            if "disconnect" not in str(exc).lower():
                raise
        finally:
            await self._remove_client(session, client)
            logger.info("Terminal {} websocket disconnected clients={}", terminal_id, len(session.clients))

    async def write(self, session: TerminalSession, data: str) -> None:
        if session.status != "running" or session.master_fd is None:
            return
        pending = memoryview(data.encode())
        logger.debug("Terminal {} input bytes={}", session.id, len(pending))
        async with session.write_lock:
            while pending:
                try:
                    written = os.write(session.master_fd, pending)
                except BlockingIOError:
                    await asyncio.sleep(0.005)
                    continue
                except OSError:
                    return
                if written <= 0:
                    await asyncio.sleep(0.005)
                    continue
                pending = pending[written:]

    async def resize(self, session: TerminalSession, rows: int, cols: int, *, lock: bool = False) -> None:
        if session.master_fd is None:
            return
        rows = max(5, rows)
        cols = max(20, cols)
        if session.rows == rows and session.cols == cols and session.layout_locked == lock:
            return
        session.rows = rows
        session.cols = cols
        session.layout_locked = lock
        self._set_size(session.master_fd, rows, cols)
        await self._broadcast(session, {"type": "layout", "terminal": session.summary()})

    async def shutdown(self) -> None:
        for terminal_id in list(self.sessions):
            await self.delete(terminal_id)

    async def _read_output(self, session: TerminalSession) -> None:
        assert session.master_fd is not None
        while True:
            try:
                chunk = await asyncio.to_thread(os.read, session.master_fd, 4096)
            except BlockingIOError:
                await asyncio.sleep(0.02)
                continue
            except OSError:
                return
            if not chunk:
                return
            text = chunk.decode(errors="replace")
            session.output += text
            session.output_version += 1
            logger.debug("Terminal {} output bytes={} version={}", session.id, len(chunk), session.output_version)
            if len(session.output) > MAX_OUTPUT_CHARS:
                session.output = session.output[-MAX_OUTPUT_CHARS:]
            await asyncio.to_thread(self._append_log, session, text)
            await self._broadcast(session, {"type": "output", "data": text, "output_version": session.output_version})

    async def _wait_for_exit(self, session: TerminalSession) -> None:
        if not session.process:
            return
        exit_code = await asyncio.to_thread(session.process.wait)
        session.status = "exited"
        session.exit_code = exit_code
        logger.info("Terminal {} exited code={}", session.id, exit_code)
        await self._broadcast(session, {"type": "status", "terminal": session.summary()})

    async def _terminate_process(self, session: TerminalSession) -> None:
        process = session.process
        if not process or process.poll() is not None:
            if session.status == "running":
                session.status = "exited"
                session.exit_code = process.poll() if process else None
            return
        try:
            os.killpg(process.pid, signal.SIGHUP)
        except ProcessLookupError:
            return
        try:
            await asyncio.wait_for(asyncio.to_thread(process.wait), timeout=1.5)
        except asyncio.TimeoutError:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        try:
            await asyncio.wait_for(asyncio.to_thread(process.wait), timeout=1.5)
        except asyncio.TimeoutError:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            await asyncio.to_thread(process.wait)
        session.status = "exited"
        session.exit_code = process.returncode
        await self._broadcast(session, {"type": "status", "terminal": session.summary()})

    async def _broadcast(self, session: TerminalSession, message: dict) -> None:
        for _client in await broadcast(session.clients, message):
            logger.warning("Terminal {} removing stale websocket client", session.id)

    async def _remove_client(self, session: TerminalSession, client: WebSocketClient) -> None:
        await remove_client(session.clients, client)

    def _set_size(self, fd: int, rows: int, cols: int) -> None:
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def _initialize_log(self, session: TerminalSession) -> None:
        if session.log_path is None:
            return
        session.log_path.parent.mkdir(parents=True, exist_ok=True)
        session.log_path.write_text("", encoding="utf-8")

    def _append_log(self, session: TerminalSession, text: str) -> None:
        if session.log_path is None:
            return
        with session.log_path.open("a", encoding="utf-8") as handle:
            handle.write(text)

    def _read_log(self, session: TerminalSession) -> str:
        if session.log_path is None or not session.log_path.exists():
            return session.output
        return session.log_path.read_text(encoding="utf-8", errors="replace")

    def _snapshot_for_client(self, session: TerminalSession) -> dict:
        snapshot = session.snapshot()
        snapshot["output"] = self._read_log(session)
        return snapshot

    def _remove_log(self, session: TerminalSession) -> None:
        if session.log_path is None:
            return
        try:
            session.log_path.unlink(missing_ok=True)
        except Exception:
            logger.warning("Failed to remove terminal log {}", session.log_path)


terminal_manager = TerminalManager()
