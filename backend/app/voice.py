import asyncio
import gc
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
_whisper_engine_key: tuple[Any, ...] | None = None
_whisper_engine_lock = asyncio.Lock()
_offline_model: Any | None = None
_offline_model_name: str | None = None
_offline_model_lock = asyncio.Lock()
_offline_transcribe_lock = asyncio.Lock()
_offline_model_unload_task: asyncio.Task | None = None
_offline_model_last_used = 0.0


@dataclass(frozen=True)
class VoiceRuntimeConfig:
    enabled: bool
    model: str
    language: str
    translation_enabled: bool
    target_language: str
    backend: str
    backend_policy: str
    direct_english_translation: bool
    min_chunk_size: float
    stop_timeout_seconds: float
    model_idle_timeout_seconds: float
    offline_beam_size: int
    offline_vad_filter: bool
    vac: bool
    vad: bool
    service_ws: str
    upstream_ws: str


LANGUAGE_ALIASES = {
    "cn": "zh",
    "zh-cn": "zh",
    "zh_cn": "zh",
    "chinese": "zh",
    "eng": "en",
    "english": "en",
}


def _normalize_language(value: str, fallback: str, *, allow_auto: bool) -> str:
    cleaned = (value or "").strip().lower()
    if not cleaned:
        return fallback
    normalized = LANGUAGE_ALIASES.get(cleaned, cleaned)
    if normalized == "auto" and not allow_auto:
        return fallback
    return normalized


def _voice_config() -> VoiceRuntimeConfig:
    from .files import read_config

    config = read_config().voice
    language = _normalize_language(config.language, "auto", allow_auto=True)
    target_language = _normalize_language(config.target_language, "en", allow_auto=False)
    translation_enabled = bool(config.translation_enabled and target_language)
    return VoiceRuntimeConfig(
        enabled=settings.voice_enabled and config.enabled,
        model=(config.model or settings.voice_model).strip() or "large-v3-turbo",
        language=language,
        translation_enabled=translation_enabled,
        target_language=target_language if translation_enabled else "",
        backend=settings.voice_backend,
        backend_policy=settings.voice_backend_policy,
        direct_english_translation=settings.voice_direct_english_translation,
        min_chunk_size=settings.voice_min_chunk_size,
        stop_timeout_seconds=settings.voice_stop_timeout_seconds,
        model_idle_timeout_seconds=settings.voice_model_idle_timeout_seconds,
        offline_beam_size=settings.voice_offline_beam_size,
        offline_vad_filter=settings.voice_offline_vad_filter,
        vac=settings.voice_vac,
        vad=settings.voice_vad,
        service_ws=settings.voice_service_ws,
        upstream_ws=settings.voice_upstream_ws,
    )


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


def _line_text(line: dict[str, Any], cfg: VoiceRuntimeConfig) -> str:
    if line.get("speaker") == -2:
        return ""
    if cfg.translation_enabled and line.get("translation"):
        return str(line.get("translation") or "").strip()
    return str(line.get("text") or "").strip()


def _state_text(payload: dict[str, Any], cfg: VoiceRuntimeConfig) -> str:
    lines = payload.get("lines")
    committed = ""
    if isinstance(lines, list):
        committed = " ".join(_line_text(line, cfg) for line in lines if isinstance(line, dict)).strip()
    buffer_key = "buffer_translation" if cfg.translation_enabled else "buffer_transcription"
    buffer_text = str(payload.get(buffer_key) or "").strip()
    return " ".join(part for part in [committed, buffer_text] if part)


def _normalize_payload(payload: dict[str, Any], cfg: VoiceRuntimeConfig) -> dict[str, str] | None:
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
        text = _state_text(payload, cfg)
        is_final = False
    if not text:
        return None
    return {"type": "final" if is_final else "partial", "text": str(text)}


def _normalize_upstream_message(message: str | bytes, cfg: VoiceRuntimeConfig) -> dict[str, str] | None:
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

    return _normalize_payload(payload, cfg)


def _voice_service_start_payload(payload: dict[str, Any], cfg: VoiceRuntimeConfig) -> dict[str, Any]:
    return {
        **payload,
        "type": "start",
        "model": cfg.model,
        "language": cfg.language,
        "translation_enabled": cfg.translation_enabled,
        "target_language": cfg.target_language,
        "offline_beam_size": cfg.offline_beam_size,
        "offline_vad_filter": cfg.offline_vad_filter,
    }


def _normalize_voice_service_message(message: str | bytes) -> dict[str, Any] | None:
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
    if not isinstance(payload, dict):
        return None
    message_type = payload.get("type")
    if message_type in {"ready", "processing", "partial", "committed", "final", "error"}:
        return payload
    if payload.get("text"):
        return {"type": "partial", "text": str(payload["text"])}
    return None


async def _connect_voice_service(websocket: WebSocket, cfg: VoiceRuntimeConfig) -> None:
    async with websockets.connect(cfg.service_ws, max_size=None) as service:
        await websocket.send_json({"type": "ready"})

        async def client_to_service() -> None:
            while True:
                try:
                    message = await websocket.receive()
                except (WebSocketDisconnect, RuntimeError):
                    return
                if message.get("type") == "websocket.disconnect":
                    return
                if "bytes" in message and message["bytes"] is not None:
                    await service.send(message["bytes"])
                    continue
                if "text" not in message or not message["text"]:
                    continue
                try:
                    payload = json.loads(message["text"])
                except json.JSONDecodeError:
                    payload = {"type": message["text"]}
                if payload.get("type") == "start":
                    payload = _voice_service_start_payload(payload, cfg)
                await service.send(json.dumps(payload))
                if payload.get("type") == "stop":
                    return

        async def service_to_client() -> None:
            async for message in service:
                normalized = _normalize_voice_service_message(message)
                if normalized:
                    if normalized.get("type") == "ready":
                        continue
                    await websocket.send_json(normalized)

        client_task = asyncio.create_task(client_to_service())
        service_task = asyncio.create_task(service_to_client())
        try:
            done, _ = await asyncio.wait([client_task, service_task], return_when=asyncio.FIRST_COMPLETED)
            if client_task in done:
                client_task.result()
                with suppress(TimeoutError):
                    await asyncio.wait_for(service_task, timeout=cfg.stop_timeout_seconds + 120)
            else:
                service_task.result()
                client_task.cancel()
                await asyncio.gather(client_task, return_exceptions=True)
        finally:
            for task in (client_task, service_task):
                if not task.done():
                    task.cancel()
            await asyncio.gather(client_task, service_task, return_exceptions=True)


def _whisper_kwargs(cfg: VoiceRuntimeConfig) -> dict[str, Any]:
    target_language = cfg.target_language
    if target_language and cfg.language == "auto" and cfg.backend_policy != "simulstreaming":
        logger.warning(
            "Ignoring voice target_language={} because WhisperLiveKit translation with "
            "language=auto requires backend_policy=simulstreaming; using transcription-only mode.",
            target_language,
        )
        target_language = ""

    return {
        "model_size": cfg.model,
        "lan": cfg.language,
        "target_language": target_language,
        "backend": cfg.backend,
        "backend_policy": cfg.backend_policy,
        "direct_english_translation": cfg.direct_english_translation,
        "min_chunk_size": cfg.min_chunk_size,
        "vac": cfg.vac,
        "vad": cfg.vad,
        "pcm_input": False,
    }


async def _get_whisper_engine(cfg: VoiceRuntimeConfig) -> Any:
    global _whisper_engine, _whisper_engine_key
    async with _whisper_engine_lock:
        kwargs = _whisper_kwargs(cfg)
        key = tuple(sorted(kwargs.items()))
        if _whisper_engine is None or _whisper_engine_key != key:
            try:
                from whisperlivekit import TranscriptionEngine
            except ImportError as exc:
                raise RuntimeError("WhisperLiveKit is not installed. Run `uv sync`.") from exc
            logger.info("Loading WhisperLiveKit voice engine with {}", kwargs)
            _whisper_engine = await asyncio.to_thread(TranscriptionEngine, **kwargs)
            _whisper_engine_key = key
        return _whisper_engine


def _release_gpu_memory() -> None:
    gc.collect()
    with suppress(Exception):
        import torch

        if torch.cuda.is_available():
            torch.cuda.empty_cache()


async def _unload_offline_model_after_idle() -> None:
    global _offline_model
    await asyncio.sleep(settings.voice_model_idle_timeout_seconds)
    async with _offline_model_lock:
        idle_for = asyncio.get_running_loop().time() - _offline_model_last_used
        if _offline_model is None or idle_for < settings.voice_model_idle_timeout_seconds:
            _schedule_offline_model_unload()
            return
        logger.info("Unloading offline voice model after {:.1f}s idle", idle_for)
        _offline_model = None
    await asyncio.to_thread(_release_gpu_memory)


def _schedule_offline_model_unload() -> None:
    global _offline_model_unload_task
    if settings.voice_model_idle_timeout_seconds <= 0:
        return
    current_task = asyncio.current_task()
    if _offline_model_unload_task and not _offline_model_unload_task.done() and _offline_model_unload_task is not current_task:
        _offline_model_unload_task.cancel()
    _offline_model_unload_task = asyncio.create_task(_unload_offline_model_after_idle())


async def _get_offline_model(cfg: VoiceRuntimeConfig) -> Any:
    global _offline_model, _offline_model_last_used, _offline_model_name
    async with _offline_model_lock:
        _offline_model_last_used = asyncio.get_running_loop().time()
        if _offline_model is None or _offline_model_name != cfg.model:
            try:
                from faster_whisper import WhisperModel
            except ImportError as exc:
                raise RuntimeError("faster-whisper is not installed. Run `uv sync`.") from exc
            if _offline_model is not None:
                logger.info("Switching offline voice model from {} to {}", _offline_model_name, cfg.model)
                _offline_model = None
                await asyncio.to_thread(_release_gpu_memory)
            logger.info("Loading offline voice model {}", cfg.model)
            _offline_model = await asyncio.to_thread(WhisperModel, cfg.model, device="auto", compute_type="default")
            _offline_model_name = cfg.model
        _schedule_offline_model_unload()
        return _offline_model


def _join_segments(segments: Any) -> str:
    return " ".join(str(segment.text).strip() for segment in segments if str(segment.text).strip()).strip()


def _transcribe_with_model(model: Any, audio_path: Path, cfg: VoiceRuntimeConfig) -> str:
    task = "translate" if cfg.translation_enabled and cfg.target_language == "en" else "transcribe"
    if cfg.translation_enabled and cfg.target_language != "en":
        logger.warning("Offline faster-whisper translation only supports English output; transcribing without translation.")
    segments, _info = model.transcribe(
        audio_path.as_posix(),
        language=cfg.language if cfg.language != "auto" else None,
        task=task,
        beam_size=cfg.offline_beam_size,
        vad_filter=cfg.offline_vad_filter,
        condition_on_previous_text=True,
    )
    return _join_segments(segments)


async def _transcribe_offline(audio_path: Path, cfg: VoiceRuntimeConfig) -> str:
    global _offline_model_last_used
    model = await _get_offline_model(cfg)
    async with _offline_transcribe_lock:
        text = await asyncio.to_thread(_transcribe_with_model, model, audio_path, cfg)
        _offline_model_last_used = asyncio.get_running_loop().time()
        _schedule_offline_model_unload()
        return " ".join(text.split())


async def _connect_offline_voice(websocket: WebSocket, cfg: VoiceRuntimeConfig) -> None:
    capture = VoiceCapture(_utc_stamp(), f"offline-{cfg.backend}", cfg.backend_policy)
    await websocket.send_json({"type": "ready"})
    try:
        while True:
            message = await websocket.receive()
            if "bytes" in message and message["bytes"] is not None:
                capture.write(message["bytes"])
                continue
            if "text" not in message or not message["text"]:
                continue
            try:
                payload = json.loads(message["text"])
            except json.JSONDecodeError:
                payload = {"type": message["text"]}
            if payload.get("type") == "start":
                capture.set_mime_type(str(payload.get("mimeType") or ""))
            if payload.get("type") == "stop":
                audio_path = capture.finish()
                if audio_path is None:
                    await _send_error(websocket, "No voice audio was received.")
                    return
                await websocket.send_json({"type": "processing"})
                text = await _transcribe_offline(audio_path, cfg)
                await websocket.send_json({"type": "final", "text": text})
                return
    finally:
        if capture.path is None:
            capture.finish()


async def _connect_whisperlivekit(websocket: WebSocket, cfg: VoiceRuntimeConfig) -> None:
    from whisperlivekit import AudioProcessor

    engine = await _get_whisper_engine(cfg)
    audio_processor = AudioProcessor(transcription_engine=engine, language=cfg.language)
    results_generator = await audio_processor.create_tasks()
    capture = VoiceCapture(_utc_stamp(), cfg.backend, cfg.backend_policy)
    await websocket.send_json({"type": "ready"})

    async def results_to_client() -> None:
        async for response in results_generator:
            payload = response.to_dict() if hasattr(response, "to_dict") else response
            if isinstance(payload, dict):
                normalized = _normalize_payload(payload, cfg)
                if normalized:
                    await websocket.send_json(normalized)
        final_text = _state_text(audio_processor.last_response_content.to_dict(), cfg)
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
                await asyncio.wait_for(results_task, timeout=cfg.stop_timeout_seconds)
            except TimeoutError:
                logger.warning(
                    "Timed out waiting {}s for final WhisperLiveKit voice results",
                    cfg.stop_timeout_seconds,
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
    cfg = _voice_config()
    if not cfg.enabled:
        await _send_error(
            websocket,
            "Voice input is disabled. Enable it in ~/.view/config.json or set VIEWER_VOICE_ENABLED=true.",
        )
        await websocket.close(code=1013)
        return

    try:
        if cfg.service_ws:
            await _connect_voice_service(websocket, cfg)
            return

        if not cfg.upstream_ws:
            await _connect_offline_voice(websocket, cfg)
            return

        async with websockets.connect(cfg.upstream_ws, max_size=None) as upstream:
            capture = VoiceCapture(_utc_stamp(), "upstream", "")
            await websocket.send_json({"type": "ready"})

            async def client_to_upstream() -> None:
                while True:
                    try:
                        message = await websocket.receive()
                    except (WebSocketDisconnect, RuntimeError):
                        return
                    if message.get("type") == "websocket.disconnect":
                        return
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
                    normalized = _normalize_upstream_message(message, cfg)
                    if normalized:
                        await websocket.send_json(normalized)

            client_task = asyncio.create_task(client_to_upstream())
            upstream_task = asyncio.create_task(upstream_to_client())
            try:
                done, _ = await asyncio.wait([client_task, upstream_task], return_when=asyncio.FIRST_COMPLETED)
                if client_task in done:
                    client_task.result()
                    with suppress(TimeoutError):
                        await asyncio.wait_for(upstream_task, timeout=cfg.stop_timeout_seconds)
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
