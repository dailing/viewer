from __future__ import annotations

import asyncio
import json
import os
import shutil
from contextlib import suppress
from dataclasses import dataclass
from typing import Any, Awaitable, Callable

from loguru import logger


CodexAppServerUpdateHandler = Callable[[str, dict[str, Any]], Awaitable[None]]


@dataclass(frozen=True)
class CodexAppServerProcessConfig:
    provider: str
    command: str
    arguments: tuple[str, ...]
    enabled: bool = True


class CodexAppServerSessionNotFound(RuntimeError):
    """Raised when a Codex app-server thread cannot be restored."""


class CodexAppServerRuntime:
    """Experimental Codex app-server JSON-RPC client.

    This is NOT ACP — it speaks Codex's native app-server protocol over stdio.
    The protocol uses thread/start, turn/start, turn/interrupt, thread/resume, etc.
    """

    def __init__(self, config: CodexAppServerProcessConfig, update_handler: CodexAppServerUpdateHandler) -> None:
        self.config = config
        self.provider = config.provider
        self.enabled = config.enabled
        self.command = config.command
        self._update_handler = update_handler
        self._process: asyncio.subprocess.Process | None = None
        self._reader_task: asyncio.Task | None = None
        self._stderr_task: asyncio.Task | None = None
        self._request_id = 0
        self._pending_requests: dict[int, asyncio.Future[dict[str, Any]]] = {}
        self._turn_waiters: dict[str, asyncio.Future[dict[str, Any]]] = {}
        self._completed_turns: dict[str, dict[str, Any]] = {}
        self._bound_threads: set[str] = set()
        self._initialized = False
        self._start_lock = asyncio.Lock()
        self._write_lock = asyncio.Lock()

    @property
    def running(self) -> bool:
        return self._process is not None and self._process.returncode is None

    def _next_request_id(self) -> int:
        self._request_id += 1
        return self._request_id

    async def start(self) -> None:
        if not self.enabled:
            raise RuntimeError(f"{self.provider} app-server is disabled")
        if self.running and self._initialized:
            return
        async with self._start_lock:
            if self.running and self._initialized:
                return
            await self._close_process()
            executable = shutil.which(self.command)
            if not executable:
                raise RuntimeError(f"{self.provider} app-server command not found: {self.command}")
            args = [executable, *self.config.arguments]
            logger.info("Starting {} app-server: {}", self.provider, " ".join(args))
            self._process = await asyncio.create_subprocess_exec(
                *args,
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            self._reader_task = asyncio.create_task(self._read_loop())
            self._stderr_task = asyncio.create_task(self._drain_stderr())
            try:
                initialize_result = await self._send_request(
                    "initialize",
                    {
                        "clientInfo": {
                            "name": "viewer_super_workspace",
                            "title": "Viewer Super Workspace",
                            "version": "0.1.0",
                        }
                    },
                )
                await self._send_notification("initialized", {})
                self._initialized = True
            except Exception:
                await self._close_process()
                raise
            logger.info(
                "{} app-server started pid={} user_agent={}",
                self.provider,
                self._process.pid,
                initialize_result.get("userAgent", ""),
            )

    async def _read_loop(self) -> None:
        assert self._process is not None and self._process.stdout is not None
        while True:
            line = await self._process.stdout.readline()
            if not line:
                break
            try:
                message = json.loads(line.decode("utf-8"))
            except json.JSONDecodeError:
                logger.warning("{} app-server invalid JSON: {}", self.provider, line[:200])
                continue
            await self._handle_message(message)
        logger.info("{} app-server stdout closed", self.provider)
        error = RuntimeError(f"{self.provider} app-server stdout closed")
        for future in (*self._pending_requests.values(), *self._turn_waiters.values()):
            if not future.done():
                future.set_exception(error)
        self._pending_requests.clear()
        self._turn_waiters.clear()

    async def _drain_stderr(self) -> None:
        assert self._process is not None and self._process.stderr is not None
        while True:
            line = await self._process.stderr.readline()
            if not line:
                return
            text = line.decode("utf-8", errors="replace").rstrip()
            if text:
                logger.debug("{} app-server stderr: {}", self.provider, text)

    async def _handle_message(self, message: dict[str, Any]) -> None:
        # JSON-RPC response
        if "id" in message and ("result" in message or "error" in message):
            request_id = message["id"]
            future = self._pending_requests.pop(request_id, None)
            if future is not None and not future.done():
                if "error" in message:
                    future.set_exception(RuntimeError(f"{self.provider} RPC error: {message['error']}"))
                else:
                    future.set_result(message.get("result", {}))
            return
        # Server-initiated requests are not part of Viewer capability support.
        # Respond explicitly so Codex fails the operation instead of waiting
        # forever for a client response that will never arrive.
        if "id" in message and message.get("method"):
            await self._send_error_response(
                message["id"],
                -32601,
                f"Viewer does not support app-server request: {message['method']}",
            )
            return
        # JSON-RPC notification
        method = message.get("method", "")
        params = message.get("params", {})
        thread_id = params.get("threadId") or params.get("thread_id") or ""
        if thread_id:
            await self._update_handler(thread_id, {"method": method, "params": params, "raw": message})
        if method == "turn/completed":
            turn = params.get("turn") if isinstance(params.get("turn"), dict) else {}
            turn_id = str(turn.get("id") or "")
            if turn_id:
                waiter = self._turn_waiters.pop(turn_id, None)
                if waiter is not None and not waiter.done():
                    waiter.set_result(turn)
                else:
                    self._completed_turns[turn_id] = turn

    async def _write_message(self, payload: dict[str, Any]) -> None:
        if not self.running or self._process is None or self._process.stdin is None:
            raise RuntimeError(f"{self.provider} app-server is not running")
        data = json.dumps(payload, ensure_ascii=False) + "\n"
        async with self._write_lock:
            self._process.stdin.write(data.encode("utf-8"))
            await self._process.stdin.drain()

    async def _send_notification(self, method: str, params: dict[str, Any]) -> None:
        await self._write_message({"method": method, "params": params})

    async def _send_error_response(self, request_id: Any, code: int, message: str) -> None:
        await self._write_message({"id": request_id, "error": {"code": code, "message": message}})

    async def _send_request(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        if not self.running or self._process is None or self._process.stdin is None:
            raise RuntimeError(f"{self.provider} app-server is not running")
        request_id = self._next_request_id()
        future: asyncio.Future[dict[str, Any]] = asyncio.Future()
        self._pending_requests[request_id] = future
        payload = {"id": request_id, "method": method, "params": params}
        await self._write_message(payload)
        try:
            return await asyncio.wait_for(future, timeout=60.0)
        except asyncio.TimeoutError:
            self._pending_requests.pop(request_id, None)
            # A timed-out control request leaves the connection state unknown:
            # app-server may still finish it later, while the client has already
            # moved on. Never send another request through that process. The next
            # operation will start a fresh initialized runtime.
            await self._close_process()
            raise RuntimeError(f"{self.provider} app-server request timed out: {method}")

    async def thread_start(self, cwd: str, model: str | None = None) -> str:
        await self.start()
        params: dict[str, Any] = {"cwd": cwd}
        if model:
            params["model"] = model
        result = await self._send_request("thread/start", params)
        thread = result.get("thread", {})
        thread_id = str(thread.get("id") or result.get("threadId") or "")
        if not thread_id:
            raise RuntimeError(f"{self.provider} app-server thread/start returned no thread id")
        self._bound_threads.add(thread_id)
        return thread_id

    async def thread_resume(self, thread_id: str, cwd: str) -> str:
        await self.start()
        if thread_id in self._bound_threads:
            return thread_id
        result = await self._send_request("thread/resume", {"threadId": thread_id, "cwd": cwd})
        resumed = result.get("thread", {})
        resumed_id = str(resumed.get("id") or thread_id)
        self._bound_threads.add(resumed_id)
        return resumed_id

    async def turn_start(self, thread_id: str, prompt: str | list[dict[str, Any]]) -> dict[str, Any]:
        await self.start()
        if isinstance(prompt, str):
            input_items = [{"type": "text", "text": prompt}]
        else:
            input_items = prompt
        result = await self._send_request("turn/start", {"threadId": thread_id, "input": input_items})
        turn = result.get("turn") if isinstance(result.get("turn"), dict) else {}
        turn_id = str(turn.get("id") or "")
        if not turn_id:
            raise RuntimeError(f"{self.provider} app-server turn/start returned no turn id")
        if turn.get("status") in {"completed", "failed", "interrupted"}:
            return {"turn": turn}
        completed = self._completed_turns.pop(turn_id, None)
        if completed is None:
            waiter: asyncio.Future[dict[str, Any]] = asyncio.Future()
            self._turn_waiters[turn_id] = waiter
            completed = await waiter
        return {"turn": completed}

    async def turn_interrupt(self, thread_id: str) -> None:
        if not self.running:
            return
        with suppress(Exception):
            await self._send_request("turn/interrupt", {"threadId": thread_id})

    async def thread_fork(self, thread_id: str, cwd: str) -> str:
        await self.start()
        result = await self._send_request("thread/fork", {"threadId": thread_id, "cwd": cwd})
        forked = result.get("thread", {})
        forked_id = str(forked.get("id") or "")
        if not forked_id:
            raise RuntimeError(f"{self.provider} app-server could not fork thread: {thread_id}")
        self._bound_threads.add(forked_id)
        return forked_id

    async def thread_list(self, cwd: str | None = None) -> dict[str, Any]:
        await self.start()
        params: dict[str, Any] = {}
        if cwd:
            params["cwd"] = cwd
        return await self._send_request("thread/list", params)

    async def _close_process(self) -> None:
        reader_task = self._reader_task
        stderr_task = self._stderr_task
        process = self._process
        self._reader_task = None
        self._stderr_task = None
        self._process = None
        self._initialized = False
        self._bound_threads.clear()
        for future in self._pending_requests.values():
            if not future.done():
                future.set_exception(RuntimeError(f"{self.provider} app-server shutting down"))
        self._pending_requests.clear()
        for future in self._turn_waiters.values():
            if not future.done():
                future.set_exception(RuntimeError(f"{self.provider} app-server shutting down"))
        self._turn_waiters.clear()
        self._completed_turns.clear()
        if reader_task is not None:
            if not reader_task.done():
                reader_task.cancel()
            with suppress(asyncio.CancelledError):
                await reader_task
        if stderr_task is not None:
            if not stderr_task.done():
                stderr_task.cancel()
            with suppress(asyncio.CancelledError):
                await stderr_task
        if process is not None:
            with suppress(ProcessLookupError):
                process.terminate()
            with suppress(asyncio.TimeoutError):
                await asyncio.wait_for(process.wait(), timeout=5)
            if process.returncode is None:
                with suppress(ProcessLookupError):
                    process.kill()
                await process.wait()

    async def shutdown(self) -> None:
        await self._close_process()
