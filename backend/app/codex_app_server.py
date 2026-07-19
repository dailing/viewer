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
        self._bound_threads: set[str] = set()
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
        if self.running:
            return
        async with self._start_lock:
            if self.running:
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
            logger.info("{} app-server started pid={}", self.provider, self._process.pid)

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
        # JSON-RPC notification
        method = message.get("method", "")
        params = message.get("params", {})
        thread_id = params.get("threadId") or params.get("thread_id") or ""
        if thread_id:
            await self._update_handler(thread_id, {"method": method, "params": params, "raw": message})

    async def _send_request(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        if not self.running or self._process is None or self._process.stdin is None:
            raise RuntimeError(f"{self.provider} app-server is not running")
        request_id = self._next_request_id()
        future: asyncio.Future[dict[str, Any]] = asyncio.Future()
        self._pending_requests[request_id] = future
        payload = {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}
        data = json.dumps(payload, ensure_ascii=False) + "\n"
        async with self._write_lock:
            self._process.stdin.write(data.encode("utf-8"))
            await self._process.stdin.drain()
        try:
            return await asyncio.wait_for(future, timeout=60.0)
        except asyncio.TimeoutError:
            self._pending_requests.pop(request_id, None)
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
        return result

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
        self._bound_threads.clear()
        for future in self._pending_requests.values():
            if not future.done():
                future.set_exception(RuntimeError(f"{self.provider} app-server shutting down"))
        self._pending_requests.clear()
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
