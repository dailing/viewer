from __future__ import annotations

import asyncio
from contextlib import suppress
from dataclasses import dataclass
import shutil
from typing import Any, Awaitable, Callable

import acp
from acp.schema import (
    AudioContentBlock,
    BlobResourceContents,
    ClientCapabilities,
    DeniedOutcome,
    EmbeddedResourceContentBlock,
    ImageContentBlock,
    Implementation,
    RequestPermissionResponse,
    ResourceContentBlock,
    TextContentBlock,
    TextResourceContents,
)
from loguru import logger


ACPUpdateHandler = Callable[[str, Any], Awaitable[None]]


@dataclass(frozen=True)
class ACPProcessConfig:
    provider: str
    command: str
    arguments: tuple[str, ...]
    enabled: bool = True
    profile: str = "default"
    yolo: bool = False


class ACPSessionNotFound(RuntimeError):
    """Raised when an ACP provider cannot restore a requested session."""


class ViewerACPClient:
    def __init__(self, provider: str, update_handler: ACPUpdateHandler) -> None:
        self.provider = provider
        self._update_handler = update_handler
        self.connection: Any | None = None

    def on_connect(self, connection: Any) -> None:
        self.connection = connection

    async def request_permission(self, session_id: str, tool_call: Any, options: list[Any], **_: Any) -> RequestPermissionResponse:
        logger.warning(
            "{} ACP permission denied because Viewer has no approval UI session={} tool={} options={}",
            self.provider,
            session_id,
            getattr(tool_call, "title", None),
            [getattr(option, "kind", None) for option in options],
        )
        return RequestPermissionResponse(outcome=DeniedOutcome(outcome="cancelled"))

    async def session_update(self, session_id: str, update: Any, **_: Any) -> None:
        await self._update_handler(session_id, update)

    async def ext_method(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        logger.debug("Ignoring unsupported {} ACP extension method {} params={}", self.provider, method, params)
        return {}

    async def ext_notification(self, method: str, params: dict[str, Any]) -> None:
        logger.debug("Ignoring unsupported {} ACP extension notification {} params={}", self.provider, method, params)

    async def write_text_file(self, **_: Any) -> None:
        return None

    async def read_text_file(self, **_: Any) -> Any:
        raise RuntimeError(f"{self.provider} ACP client filesystem access is not enabled")

    async def create_terminal(self, **_: Any) -> Any:
        raise RuntimeError(f"{self.provider} ACP client terminal access is not enabled")

    async def terminal_output(self, **_: Any) -> Any:
        raise RuntimeError(f"{self.provider} ACP client terminal access is not enabled")

    async def release_terminal(self, **_: Any) -> None:
        return None

    async def wait_for_terminal_exit(self, **_: Any) -> Any:
        raise RuntimeError(f"{self.provider} ACP client terminal access is not enabled")

    async def kill_terminal(self, **_: Any) -> None:
        return None


class ACPRuntime:
    def __init__(self, config: ACPProcessConfig, update_handler: ACPUpdateHandler) -> None:
        self.config = config
        self.provider = config.provider
        self.enabled = config.enabled
        self.profile = config.profile
        self.command = config.command
        self.yolo = config.yolo
        self._client = ViewerACPClient(config.provider, update_handler)
        self._connection: Any | None = None
        self._process: asyncio.subprocess.Process | None = None
        self._process_context: Any | None = None
        self._stderr_task: asyncio.Task | None = None
        self._bound_sessions: set[str] = set()
        self.agent_capabilities: Any | None = None
        self.agent_info: Any | None = None
        self._start_lock = asyncio.Lock()

    @property
    def running(self) -> bool:
        return self._connection is not None and self._process is not None and self._process.returncode is None

    def capabilities_snapshot(self) -> dict[str, Any]:
        capabilities = self.agent_capabilities
        if capabilities is None:
            return {}
        dump = getattr(capabilities, "model_dump", None)
        return dump(mode="json", by_alias=True, exclude_none=True) if callable(dump) else {}

    def _require_session_capability(self, name: str) -> None:
        session_capabilities = getattr(self.agent_capabilities, "session_capabilities", None)
        if getattr(session_capabilities, name, None) is None:
            raise RuntimeError(f"{self.provider} ACP does not advertise session/{name} support")

    def _agent_arguments(self) -> list[str]:
        return list(self.config.arguments)

    async def start(self) -> None:
        if not self.enabled:
            raise RuntimeError(f"{self.provider} ACP is disabled")
        if self.running:
            return
        async with self._start_lock:
            if self.running:
                return
            await self._close_process()
            executable = shutil.which(self.command)
            if not executable:
                raise RuntimeError(f"{self.provider} ACP command was not found: {self.command}")
            arguments = self._agent_arguments()
            context = acp.spawn_agent_process(self._client, executable, *arguments, use_unstable_protocol=True)
            try:
                connection, process = await context.__aenter__()
                self._process_context = context
                self._connection = connection
                self._process = process
                self._stderr_task = asyncio.create_task(self._drain_stderr(process))
                initialized = await asyncio.wait_for(
                    connection.initialize(
                        protocol_version=acp.PROTOCOL_VERSION,
                        client_capabilities=ClientCapabilities(),
                        client_info=Implementation(name="super-workspace-viewer", version="0.1"),
                    ),
                    timeout=30,
                )
            except Exception:
                await self._close_process()
                raise
            agent_name = initialized.agent_info.name if initialized.agent_info else f"{self.provider}-agent"
            self.agent_capabilities = initialized.agent_capabilities
            self.agent_info = initialized.agent_info
            logger.info(
                "{} ACP runtime started profile={} pid={} protocol={} agent={} yolo={}",
                self.provider,
                self.profile,
                process.pid,
                acp.PROTOCOL_VERSION,
                agent_name,
                self.yolo,
            )

    async def _drain_stderr(self, process: asyncio.subprocess.Process) -> None:
        if process.stderr is None:
            return
        while True:
            line = await process.stderr.readline()
            if not line:
                return
            text = line.decode("utf-8", errors="replace").rstrip()
            if text:
                logger.debug("{} ACP [{}] {}", self.provider, self.profile, text)

    def _require_connection(self) -> Any:
        if not self.running or self._connection is None:
            raise RuntimeError(f"{self.provider} ACP runtime is not running")
        return self._connection

    async def new_session(self, cwd: str, model: str | None = None) -> str:
        await self.start()
        connection = self._require_connection()
        response = await connection.new_session(cwd=cwd)
        session_id = str(response.session_id)
        self._bound_sessions.add(session_id)
        if model:
            await connection.set_session_model(model_id=model, session_id=session_id)
        return session_id

    async def ensure_session(self, session_id: str, cwd: str, model: str | None = None) -> None:
        await self.start()
        if session_id not in self._bound_sessions:
            connection = self._require_connection()
            response = await connection.load_session(cwd=cwd, session_id=session_id)
            # agent-client-protocol 0.9 deserializes a JSON-RPC null result as
            # an all-default LoadSessionResponse instead of preserving None.
            # An ACP SDK 0.9 JSON-RPC null becomes an all-default Pydantic
            # response. A real load response has at least one explicit field.
            response_fields = getattr(response, "model_fields_set", None)
            if response_fields is None:
                response_fields = getattr(response, "__fields_set__", None)
            if response is None or response_fields == set():
                raise ACPSessionNotFound(f"{self.provider} ACP session not found: {session_id}")
            self._bound_sessions.add(session_id)
        if model:
            await self._require_connection().set_session_model(model_id=model, session_id=session_id)

    @staticmethod
    def _content_block(value: Any) -> Any:
        if not isinstance(value, dict):
            return value
        block_type = value.get("type")
        if block_type == "text":
            return TextContentBlock.model_validate(value)
        if block_type == "image":
            return ImageContentBlock.model_validate(value)
        if block_type == "audio":
            return AudioContentBlock.model_validate(value)
        if block_type == "resource_link":
            return ResourceContentBlock.model_validate(value)
        if block_type == "resource":
            resource = value.get("resource")
            if not isinstance(resource, dict):
                raise ValueError("ACP embedded resource requires a resource object")
            parsed = dict(value)
            parsed["resource"] = (
                BlobResourceContents.model_validate(resource)
                if "blob" in resource
                else TextResourceContents.model_validate(resource)
            )
            return EmbeddedResourceContentBlock.model_validate(parsed)
        raise ValueError(f"Unsupported ACP content block type: {block_type}")

    def _prompt_capability(self, name: str) -> bool:
        prompt_capabilities = getattr(self.agent_capabilities, "prompt_capabilities", None)
        return bool(getattr(prompt_capabilities, name, False))

    def _supported_prompt_block(self, block: Any) -> Any:
        if isinstance(block, TextContentBlock):
            return block
        if isinstance(block, ImageContentBlock):
            if not self._prompt_capability("image"):
                raise RuntimeError(f"{self.provider} ACP does not advertise image prompt support")
            return block
        if isinstance(block, AudioContentBlock):
            if not self._prompt_capability("audio"):
                raise RuntimeError(f"{self.provider} ACP does not advertise audio prompt support")
            return block
        if isinstance(block, EmbeddedResourceContentBlock):
            if self._prompt_capability("embedded_context"):
                return block
            resource = block.resource
            text = getattr(resource, "text", None)
            uri = str(getattr(resource, "uri", None) or "embedded-resource")
            if isinstance(text, str):
                return acp.text_block(f"Resource {uri}:\n{text}")
            raise RuntimeError(f"{self.provider} ACP does not advertise binary embedded-resource prompt support")
        if isinstance(block, ResourceContentBlock):
            if self._prompt_capability("embedded_context"):
                return block
            return acp.text_block(f"Resource link: {block.name} ({block.uri})")
        return block

    async def prompt(self, session_id: str, prompt: str | list[Any]) -> Any:
        await self.start()
        parsed = [acp.text_block(prompt)] if isinstance(prompt, str) else [self._content_block(value) for value in prompt]
        blocks = [self._supported_prompt_block(value) for value in parsed]
        if not blocks:
            raise ValueError("ACP prompt requires at least one content block")
        return await self._require_connection().prompt(prompt=blocks, session_id=session_id)

    async def list_sessions(self, cwd: str | None = None, cursor: str | None = None) -> Any:
        await self.start()
        self._require_session_capability("list")
        return await self._require_connection().list_sessions(cwd=cwd, cursor=cursor)

    async def fork_session(self, session_id: str, cwd: str) -> str:
        await self.start()
        self._require_session_capability("fork")
        response = await self._require_connection().fork_session(cwd=cwd, session_id=session_id)
        forked_id = str(response.session_id)
        if not forked_id:
            raise RuntimeError(f"{self.provider} ACP could not fork session: {session_id}")
        self._bound_sessions.add(forked_id)
        return forked_id

    async def resume_session(self, session_id: str, cwd: str) -> str:
        await self.start()
        self._require_session_capability("resume")
        response = await self._require_connection().resume_session(cwd=cwd, session_id=session_id)
        resumed_id = str(getattr(response, "session_id", None) or session_id)
        self._bound_sessions.add(resumed_id)
        return resumed_id

    async def set_mode(self, session_id: str, mode_id: str) -> None:
        await self.start()
        await self._require_connection().set_session_mode(mode_id=mode_id, session_id=session_id)

    async def cancel(self, session_id: str) -> None:
        if self.running:
            await self._require_connection().cancel(session_id=session_id)

    async def close_session(self, session_id: str) -> None:
        if self.running and session_id in self._bound_sessions:
            with suppress(Exception):
                await self._require_connection().close_session(session_id=session_id)
        self._bound_sessions.discard(session_id)

    async def _close_process(self) -> None:
        context = self._process_context
        stderr_task = self._stderr_task
        self._connection = None
        self._process = None
        self._process_context = None
        self._stderr_task = None
        self._bound_sessions.clear()
        self.agent_capabilities = None
        self.agent_info = None
        if context is not None:
            with suppress(Exception):
                await context.__aexit__(None, None, None)
        if stderr_task is not None:
            if not stderr_task.done():
                stderr_task.cancel()
            with suppress(asyncio.CancelledError):
                await stderr_task

    async def shutdown(self) -> None:
        await self._close_process()
        logger.info("{} ACP runtime stopped profile={}", self.provider, self.profile)
