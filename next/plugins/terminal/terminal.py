"""viewer.terminal — PTY sessions as bus instances (framework appendix A.4).

Runtime = PTY session inside this plugin process; view = frontend pane
(view/runtime split: closing a pane never kills the PTY, reopening
reconnects via snapshot + live output).

Channels:
- slots: ``terminal:_:create`` (RPC), ``terminal:_:list`` (RPC),
  ``terminal:*:input`` (RPC or fire-and-forget publish), ``terminal:*:resize``
  (publish), ``terminal:*:kill`` (RPC or publish), ``terminal:*:snapshot``
  (RPC, explicit paginated history per framework section 5.6).
- emits: ``terminal:{id}:output`` (event, incremental-UTF8 text chunks) and
  ``terminal:{id}:status`` (mailbox, full value replace).

PTY content history lives in an in-memory ring buffer (this plugin is the
source of truth); terminal metadata is mirrored best-effort to the
instance-store (C2) without depending on it.
"""

from __future__ import annotations

import asyncio
import codecs
import fcntl
import json
import logging
import os
import pty
import struct
import subprocess
import termios
import time
from collections import deque
from dataclasses import dataclass, field
from typing import Any

from sdk import Plugin, slot
from sdk.plugin import Ctx

logger = logging.getLogger(__name__)

MANIFEST = {
    "id": "terminal",
    "version": "0.1.0",
    "slots": {
        "terminal:_:create": {"summary": "spawn a PTY; RPC -> {id}"},
        "terminal:_:list": {"summary": "list terminals; RPC -> [{id, state, ...}]"},
        "terminal:*:input": {"value": {"data": "str — keystrokes/paste"}},
        "terminal:*:resize": {"value": {"cols": "int", "rows": "int"}},
        "terminal:*:kill": {"summary": "terminate the PTY"},
        "terminal:*:snapshot": {"summary": "scrollback history; RPC {limit?} -> {entries}"},
    },
    "emits": {
        "terminal:*:output": {"value": {"seq": "int", "ts": "ms", "data": "str"}},
        "terminal:*:status": {"mailbox": "full value: state/pid/cwd/shell/cols/rows/exit_code"},
    },
}

DEFAULT_COLS = 80
DEFAULT_ROWS = 24
RING_CHUNKS = 1000  # chunks kept per terminal (snapshot source)
READ_SIZE = 65536
# Output coalescing: rapid PTY bursts (full-screen TUI redraws split across
# many reads) are merged into one frame per flush. The interval keeps
# interactive echo latency imperceptible; the char cap keeps a single frame
# safely under the kernel's 1 MiB wire limit even after JSON escaping
# (escape-heavy data inflates up to ~6x: one ESC byte becomes six chars).
FLUSH_INTERVAL = 0.03
FLUSH_CHARS = 128 * 1024
# Snapshot responses are byte-budgeted (serialized JSON) so the RPC reply can
# never exceed the kernel frame limit and silently time out the caller.
SNAPSHOT_BUDGET = 800_000


def _become_controlling_tty() -> None:
    """Popen preexec: new session + make stdin (the PTY slave) controlling."""

    os.setsid()
    fcntl.ioctl(0, termios.TIOCSCTTY, 0)


def _winsize(cols: int, rows: int) -> bytes:
    return struct.pack("HHHH", rows, cols, 0, 0)


@dataclass
class TerminalSession:
    id: str
    proc: subprocess.Popen[bytes]
    master_fd: int
    cwd: str
    shell: str
    cols: int
    rows: int
    created_ts: int
    decoder: codecs.IncrementalDecoder = field(
        default_factory=lambda: codecs.getincrementaldecoder("utf-8")(errors="replace")
    )
    ring: deque[dict[str, Any]] = field(default_factory=lambda: deque(maxlen=RING_CHUNKS))
    seq: int = 0
    exit_code: int | None = None
    pending: list[str] = field(default_factory=list)
    pending_chars: int = 0
    flush_handle: asyncio.TimerHandle | None = None

    @property
    def running(self) -> bool:
        return self.exit_code is None

    def status_value(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "state": "running" if self.running else "exited",
            "exit_code": self.exit_code,
            "pid": self.proc.pid,
            "cwd": self.cwd,
            "shell": self.shell,
            "cols": self.cols,
            "rows": self.rows,
            "created_ts": self.created_ts,
        }


class TerminalPlugin(Plugin):
    manifest = MANIFEST

    def __init__(self, **client_kwargs: Any) -> None:
        super().__init__(**client_kwargs)
        self.sessions: dict[str, TerminalSession] = {}
        self._next_id = 1
        self._reaper_task: asyncio.Task[None] | None = None

    # -------------------------------------------------------------- lifecycle

    async def on_start(self) -> None:
        self._reaper_task = asyncio.create_task(self._reaper_loop())

    async def on_stop(self) -> None:
        if self._reaper_task is not None:
            self._reaper_task.cancel()
        for session in list(self.sessions.values()):
            await self._kill_session(session)

    # ------------------------------------------------------------------ slots

    @slot("terminal:_:create")
    async def create(self, ctx: Ctx) -> None:
        value = ctx.value if isinstance(ctx.value, dict) else {}
        cwd = str(value.get("cwd") or os.path.expanduser("~"))
        shell = str(value.get("shell") or os.environ.get("SHELL") or "/bin/bash")
        cols = int(value.get("cols") or DEFAULT_COLS)
        rows = int(value.get("rows") or DEFAULT_ROWS)
        if not os.path.isdir(cwd):
            await ctx.respond_error("bad_cwd", f"cwd does not exist: {cwd}")
            return
        try:
            session = self._spawn(cwd, shell, cols, rows)
        except OSError as error:
            await ctx.respond_error("spawn_failed", str(error))
            return
        self.sessions[session.id] = session
        assert self.client is not None
        await self.client.set(f"terminal:{session.id}:status", session.status_value())
        await self._mirror_instance_state(session)
        await ctx.respond({"id": session.id})

    @slot("terminal:_:list")
    async def list_terminals(self, ctx: Ctx) -> None:
        await ctx.respond([s.status_value() for s in self.sessions.values()])

    @slot("terminal:*:input")
    async def input(self, ctx: Ctx) -> None:
        session = self._session_for(ctx)
        value = ctx.value if isinstance(ctx.value, dict) else {}
        data = value.get("data")
        if session is None:
            await ctx.respond_error("no_such_terminal", ctx.channel)
            return
        if not isinstance(data, str) or data == "":
            await ctx.respond_error("bad_input", "value.data must be a non-empty string")
            return
        try:
            os.write(session.master_fd, data.encode("utf-8", errors="replace"))
        except OSError as error:
            await ctx.respond_error("write_failed", str(error))
            return
        await ctx.respond({"ok": True})

    @slot("terminal:*:resize")
    async def resize(self, ctx: Ctx) -> None:
        session = self._session_for(ctx)
        value = ctx.value if isinstance(ctx.value, dict) else {}
        try:
            cols = int(value["cols"])
            rows = int(value["rows"])
        except (KeyError, TypeError, ValueError):
            await ctx.respond_error("bad_resize", "value.cols/value.rows must be ints")
            return
        if session is None:
            await ctx.respond_error("no_such_terminal", ctx.channel)
            return
        fcntl.ioctl(session.master_fd, termios.TIOCSWINSZ, _winsize(cols, rows))
        session.cols = cols
        session.rows = rows
        assert self.client is not None
        await self.client.set(f"terminal:{session.id}:status", session.status_value())
        await ctx.respond({"ok": True})

    @slot("terminal:*:kill")
    async def kill(self, ctx: Ctx) -> None:
        session = self._session_for(ctx)
        if session is None:
            await ctx.respond_error("no_such_terminal", ctx.channel)
            return
        await self._kill_session(session)
        await ctx.respond({"ok": True})

    @slot("terminal:*:snapshot")
    async def snapshot(self, ctx: Ctx) -> None:
        session = self._session_for(ctx)
        if session is None:
            await ctx.respond_error("no_such_terminal", ctx.channel)
            return
        value = ctx.value if isinstance(ctx.value, dict) else {}
        limit = int(value.get("limit", 200))
        before_seq = value.get("before_seq")
        # Byte-budgeted (serialized JSON, newest-first): the response must
        # stay under the kernel frame limit no matter how large the ring's
        # chunks are — an oversized reply is rejected by the kernel and the
        # RPC caller just hangs until timeout.
        entries: list[dict[str, Any]] = []
        budget = SNAPSHOT_BUDGET
        for entry in reversed(session.ring):
            if len(entries) >= limit:
                break
            if before_seq is not None and entry["seq"] >= before_seq:
                continue
            size = len(json.dumps(entry))
            if entries and size > budget:
                break  # keep at least the newest entry; single entries are
                # always safe because flushes are capped far below the limit
            entries.append(entry)
            budget -= size
        entries.reverse()
        await ctx.respond({"entries": entries})

    # ------------------------------------------------------------ PTY plumbing

    def _spawn(self, cwd: str, shell: str, cols: int, rows: int) -> TerminalSession:
        master_fd, slave_fd = pty.openpty()
        fcntl.ioctl(slave_fd, termios.TIOCSWINSZ, _winsize(cols, rows))
        env = {**os.environ, "TERM": "xterm-256color"}
        try:
            proc = subprocess.Popen(
                [shell],
                stdin=slave_fd,
                stdout=slave_fd,
                stderr=slave_fd,
                cwd=cwd,
                env=env,
                close_fds=True,
                preexec_fn=_become_controlling_tty,
            )
        finally:
            os.close(slave_fd)
        session = TerminalSession(
            id=str(self._next_id),
            proc=proc,
            master_fd=master_fd,
            cwd=cwd,
            shell=shell,
            cols=cols,
            rows=rows,
            created_ts=int(time.time() * 1000),
        )
        self._next_id += 1
        loop = asyncio.get_running_loop()
        loop.add_reader(master_fd, self._on_readable, session)
        logger.info("terminal %s spawned: pid=%s shell=%s cwd=%s", session.id, proc.pid, shell, cwd)
        return session

    def _on_readable(self, session: TerminalSession) -> None:
        try:
            data = os.read(session.master_fd, READ_SIZE)
        except OSError:
            data = b""  # EIO: child side closed
        if not data:
            tail = session.decoder.decode(b"", final=True)
            if tail:
                session.pending.append(tail)
            self._flush(session)  # never lose the final output
            self._reap(session)
            return
        text = session.decoder.decode(data)
        if text == "":
            return  # incomplete multi-byte sequence — wait for the rest
        if session.pending and session.pending_chars + len(text) > FLUSH_CHARS:
            self._flush(session)  # keep every frame within the char budget
        session.pending.append(text)
        session.pending_chars += len(text)
        if session.pending_chars >= FLUSH_CHARS:
            self._flush(session)
        elif session.flush_handle is None:
            session.flush_handle = asyncio.get_running_loop().call_later(
                FLUSH_INTERVAL, self._flush, session
            )

    def _flush(self, session: TerminalSession) -> None:
        """Emit buffered output as one coalesced frame (ring + live event)."""

        if session.flush_handle is not None:
            session.flush_handle.cancel()
            session.flush_handle = None
        if not session.pending:
            return
        text = "".join(session.pending)
        session.pending.clear()
        session.pending_chars = 0
        session.seq += 1
        entry = {"seq": session.seq, "ts": int(time.time() * 1000), "data": text}
        session.ring.append(entry)
        if self.client is not None and self.client.connected:
            asyncio.get_running_loop().create_task(
                self.client.publish(f"terminal:{session.id}:output", entry)
            )

    async def _reaper_loop(self) -> None:
        while True:
            await asyncio.sleep(0.5)
            for session in list(self.sessions.values()):
                if session.running and session.proc.poll() is not None:
                    self._reap(session)

    def _reap(self, session: TerminalSession) -> None:
        if not session.running:
            return
        exit_code = session.proc.wait()
        session.exit_code = exit_code
        try:
            asyncio.get_running_loop().remove_reader(session.master_fd)
        except Exception:  # noqa: BLE001 - fd may already be gone
            pass
        logger.info("terminal %s exited: code=%s", session.id, exit_code)
        if self.client is not None and self.client.connected:
            asyncio.get_running_loop().create_task(
                self.client.set(f"terminal:{session.id}:status", session.status_value())
            )

    async def _kill_session(self, session: TerminalSession) -> None:
        if session.flush_handle is not None:
            session.flush_handle.cancel()
            session.flush_handle = None
        if session.running:
            try:
                session.proc.terminate()
            except ProcessLookupError:
                pass
            try:
                await asyncio.wait_for(
                    asyncio.get_running_loop().run_in_executor(None, session.proc.wait), 2.0
                )
            except asyncio.TimeoutError:
                session.proc.kill()
                await asyncio.get_running_loop().run_in_executor(None, session.proc.wait)
            self._reap(session)
        try:
            os.close(session.master_fd)
        except OSError:
            pass
        self.sessions.pop(session.id, None)
        if self.client is not None and self.client.connected:
            await self.client.set(
                f"terminal:{session.id}:status", {**session.status_value(), "state": "killed"}
            )
            await self._delete_instance_state(session)

    # ---------------------------------------------------------------- helpers

    def _session_for(self, ctx: Ctx) -> TerminalSession | None:
        parts = ctx.channel.split(":")
        if len(parts) < 2:
            return None
        return self.sessions.get(parts[1])

    async def _mirror_instance_state(self, session: TerminalSession) -> None:
        """Best-effort C2 metadata mirror (framework A.4). Fire-and-forget
        publish — the instance-store is a soft dependency; blocking on an RPC
        when C2 is absent would stall creation for the full RPC timeout."""

        if self.client is None or not self.client.connected:
            return
        try:
            await self.client.publish(
                "instance:_:set",
                {
                    "plugin": "terminal",
                    "instance": session.id,
                    "value": {"cwd": session.cwd, "shell": session.shell},
                },
            )
        except Exception:  # noqa: BLE001 - soft dependency on instance-store
            logger.debug("instance-store mirror skipped", exc_info=True)

    async def _delete_instance_state(self, session: TerminalSession) -> None:
        if self.client is None or not self.client.connected:
            return
        try:
            await self.client.publish(
                "instance:_:delete", {"plugin": "terminal", "instance": session.id}
            )
        except Exception:  # noqa: BLE001
            logger.debug("instance-store delete skipped", exc_info=True)
