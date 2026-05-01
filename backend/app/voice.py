import asyncio
import json
from contextlib import suppress
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import websockets
from fastapi import WebSocket
from loguru import logger
from starlette.websockets import WebSocketDisconnect

from .config import settings
from .logging import DEFAULT_LOG_DIR, current_log_path

_whisper_engine: Any | None = None
_whisper_engine_lock = asyncio.Lock()


def _utc_stamp() -> str:
    return datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S-%fZ")


def _voice_log_dir() -> Path:
    log_path = current_log_path()
    base_dir = log_path.parent if log_path else DEFAULT_LOG_DIR
    path = base_dir / "voice"
    path.mkdir(parents=True, exist_ok=True)
    return path


def _extension_for_mime(mime_type: str) -> str:
    normalized = mime_type.split(";", 1)[0].strip().lower()
    if normalized == "audio/mp4":
        return ".m4a"
    if normalized in {"audio/ogg", "application/ogg"}:
        return ".ogg"
    if normalized == "audio/wav":
        return ".wav"
    return ".webm"


@dataclass
class VoiceCapture:
    started_at: str
    backend: str
    backend_policy: str
    mime_type: str = ""
    chunks: int = 0
    bytes_written: int = 0
    path: Path | None = None
    temp_path: Path | None = None

    def set_mime_type(self, mime_type: str) -> None:
        if mime_type and not self.mime_type:
            self.mime_type = mime_type

    def write(self, chunk: bytes) -> None:
        if not chunk:
            return
        if self.temp_path is None:
            self.temp_path = _voice_log_dir() / f"voice-{self.started_at}.part"
        with self.temp_path.open("ab") as audio_file:
            audio_file.write(chunk)
        self.chunks += 1
        self.bytes_written += len(chunk)

    def finish(self) -> Path | None:
        if self.temp_path is None or self.bytes_written == 0:
            return None
        finished_at = _utc_stamp()
        extension = _extension_for_mime(self.mime_type)
        final_path = _voice_log_dir() / f"voice-{finished_at}{extension}"
        self.temp_path.replace(final_path)
        self.path = final_path
        metadata_path = final_path.with_suffix(f"{final_path.suffix}.json")
        metadata = {
            "started_at": self.started_at,
            "finished_at": finished_at,
            "mime_type": self.mime_type,
            "chunks": self.chunks,
            "bytes": self.bytes_written,
            "backend": self.backend,
            "backend_policy": self.backend_policy,
            "file": final_path.name,
        }
        metadata_path.write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
        logger.info("Saved voice debug audio to {} ({} bytes, {} chunks)", final_path, self.bytes_written, self.chunks)
        return final_path


async def _send_error(websocket: WebSocket, message: str) -> None:
    await websocket.send_json({"type": "error", "message": message})


def _line_text(line: dict[str, Any]) -> str:
    if line.get("speaker") == -2:
        return ""
    if settings.voice_target_language and line.get("translation"):
        return str(line.get("translation") or "").strip()
    return str(line.get("text") or "").strip()


def _state_text(payload: dict[str, Any]) -> str:
    lines = payload.get("lines")
    committed = ""
    if isinstance(lines, list):
        committed = " ".join(_line_text(line) for line in lines if isinstance(line, dict)).strip()
    buffer_key = "buffer_translation" if settings.voice_target_language else "buffer_transcription"
    buffer_text = str(payload.get(buffer_key) or "").strip()
    return " ".join(part for part in [committed, buffer_text] if part)


def _normalize_payload(payload: dict[str, Any]) -> dict[str, str] | None:
    if payload.get("type") in {"config", "ready_to_stop"}:
        return None
    if payload.get("error"):
        return {"type": "error", "message": str(payload["error"])}

    text = payload.get("text") or payload.get("transcript") or payload.get("sentence") or ""
    message_type = payload.get("type") or payload.get("event") or ""
    is_final = bool(payload.get("final") or payload.get("is_final") or message_type in {"final", "segment_end"})
    if not text and isinstance(payload.get("segments"), list):
        text = "".join(str(segment.get("text", "")) for segment in payload["segments"] if isinstance(segment, dict))
    if isinstance(payload.get("lines"), list):
        text = _state_text(payload)
        is_final = False
    if not text:
        return None
    return {"type": "final" if is_final else "partial", "text": str(text)}


def _normalize_upstream_message(message: str | bytes) -> dict[str, str] | None:
    if isinstance(message, bytes):
        with suppress(UnicodeDecodeError):
            message = message.decode("utf-8")
        if isinstance(message, bytes):
            return None
    try:
        payload = json.loads(message)
    except json.JSONDecodeError:
        text = message.strip()
        return {"type": "partial", "text": text} if text else None

    return _normalize_payload(payload)


def _whisper_kwargs() -> dict[str, Any]:
    target_language = settings.voice_target_language
    if target_language and settings.voice_language == "auto" and settings.voice_backend_policy != "simulstreaming":
        logger.warning(
            "Ignoring VIEWER_VOICE_TARGET_LANGUAGE={} because WhisperLiveKit translation with "
            "VIEWER_VOICE_LANGUAGE=auto requires backend_policy=simulstreaming; using transcription-only mode.",
            target_language,
        )
        target_language = ""

    return {
        "model_size": settings.voice_model,
        "lan": settings.voice_language,
        "target_language": target_language,
        "backend": settings.voice_backend,
        "backend_policy": settings.voice_backend_policy,
        "direct_english_translation": settings.voice_direct_english_translation,
        "min_chunk_size": settings.voice_min_chunk_size,
        "vac": settings.voice_vac,
        "vad": settings.voice_vad,
        "pcm_input": False,
    }


async def _get_whisper_engine() -> Any:
    global _whisper_engine
    async with _whisper_engine_lock:
        if _whisper_engine is None:
            try:
                from whisperlivekit import TranscriptionEngine
            except ImportError as exc:
                raise RuntimeError("WhisperLiveKit is not installed. Run `uv sync`.") from exc
            logger.info("Loading WhisperLiveKit voice engine with {}", _whisper_kwargs())
            _whisper_engine = await asyncio.to_thread(TranscriptionEngine, **_whisper_kwargs())
        return _whisper_engine


async def _connect_whisperlivekit(websocket: WebSocket) -> None:
    from whisperlivekit import AudioProcessor

    engine = await _get_whisper_engine()
    audio_processor = AudioProcessor(transcription_engine=engine, language=settings.voice_language)
    results_generator = await audio_processor.create_tasks()
    capture = VoiceCapture(_utc_stamp(), settings.voice_backend, settings.voice_backend_policy)
    await websocket.send_json({"type": "ready"})

    async def results_to_client() -> None:
        async for response in results_generator:
            payload = response.to_dict() if hasattr(response, "to_dict") else response
            if isinstance(payload, dict):
                normalized = _normalize_payload(payload)
                if normalized:
                    await websocket.send_json(normalized)
        final_text = _state_text(audio_processor.last_response_content.to_dict())
        if final_text:
            await websocket.send_json({"type": "final", "text": final_text})

    async def client_to_processor() -> None:
        while True:
            message = await websocket.receive()
            if "bytes" in message and message["bytes"] is not None:
                chunk = message["bytes"]
                capture.write(chunk)
                await audio_processor.process_audio(chunk)
            elif "text" in message and message["text"]:
                try:
                    payload = json.loads(message["text"])
                except json.JSONDecodeError:
                    payload = {"type": message["text"]}
                if payload.get("type") == "start":
                    capture.set_mime_type(str(payload.get("mimeType") or ""))
                if payload.get("type") == "stop":
                    await audio_processor.process_audio(b"")
                    return

    results_task = asyncio.create_task(results_to_client())
    client_task = asyncio.create_task(client_to_processor())
    try:
        done, _ = await asyncio.wait([results_task, client_task], return_when=asyncio.FIRST_COMPLETED)
        if client_task in done:
            client_task.result()
            try:
                await asyncio.wait_for(results_task, timeout=settings.voice_stop_timeout_seconds)
            except TimeoutError:
                logger.warning(
                    "Timed out waiting {}s for final WhisperLiveKit voice results",
                    settings.voice_stop_timeout_seconds,
                )
        else:
            results_task.result()
            client_task.cancel()
            await asyncio.gather(client_task, return_exceptions=True)
    finally:
        capture.finish()
        for task in (results_task, client_task):
            if not task.done():
                task.cancel()
        await asyncio.gather(results_task, client_task, return_exceptions=True)
        await audio_processor.cleanup()


async def connect_voice(websocket: WebSocket) -> None:
    await websocket.accept()
    if not settings.voice_enabled:
        await _send_error(
            websocket,
            "Voice input is disabled. Set VIEWER_VOICE_ENABLED=true to enable WhisperLiveKit voice input.",
        )
        await websocket.close(code=1013)
        return

    try:
        if not settings.voice_upstream_ws:
            await _connect_whisperlivekit(websocket)
            return

        async with websockets.connect(settings.voice_upstream_ws, max_size=None) as upstream:
            capture = VoiceCapture(_utc_stamp(), "upstream", "")
            await websocket.send_json({"type": "ready"})

            async def client_to_upstream() -> None:
                while True:
                    message = await websocket.receive()
                    if "bytes" in message and message["bytes"] is not None:
                        chunk = message["bytes"]
                        capture.write(chunk)
                        await upstream.send(chunk)
                    elif "text" in message and message["text"]:
                        try:
                            payload = json.loads(message["text"])
                        except json.JSONDecodeError:
                            payload = {"type": message["text"]}
                        if payload.get("type") == "start":
                            capture.set_mime_type(str(payload.get("mimeType") or ""))
                        if payload.get("type") == "stop":
                            await upstream.close()
                            return

            async def upstream_to_client() -> None:
                async for message in upstream:
                    normalized = _normalize_upstream_message(message)
                    if normalized:
                        await websocket.send_json(normalized)

            client_task = asyncio.create_task(client_to_upstream())
            upstream_task = asyncio.create_task(upstream_to_client())
            try:
                done, _ = await asyncio.wait([client_task, upstream_task], return_when=asyncio.FIRST_COMPLETED)
                if client_task in done:
                    client_task.result()
                    with suppress(TimeoutError):
                        await asyncio.wait_for(upstream_task, timeout=settings.voice_stop_timeout_seconds)
                else:
                    upstream_task.result()
                    client_task.cancel()
                    await asyncio.gather(client_task, return_exceptions=True)
            finally:
                capture.finish()
    except WebSocketDisconnect:
        return
    except Exception as exc:
        logger.exception("Voice WebSocket failed")
        with suppress(Exception):
            await _send_error(websocket, str(exc))
    finally:
        with suppress(Exception):
            await websocket.close()
