"""Viewer-owned LLM provider chain.

This module is the project's single OpenAI-compatible chat-completions
interface. Internal consumers (the Super Workspace dispatcher today, the
chat-context summarizer later) call :func:`chat_completion` instead of
talking to one hard-coded endpoint.

Behavior:
- Providers come from ``super_workspace.dispatch_profiles`` in Viewer
  config. List order is priority: the first enabled, non-frozen provider
  handles each call.
- A provider that fails (HTTP error, connection error, or malformed
  response) is frozen for ``super_workspace.llm_provider_freeze_seconds``
  (default 1 hour) and the chain falls through to the next provider.
- Freeze state persists in ``VIEWER_DATA_DIR/llm-provider-health.json`` so
  restarts do not reset cooldowns, mirroring the agent target
  health/cooldown pattern in ``super_workspace_target_health``.
"""

from __future__ import annotations

import json
import os
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from .storage import DATA_DIR

DEFAULT_LLM_URL = "http://127.0.0.1:11434/v1/chat/completions"
DEFAULT_LLM_MODEL = "gemma4:26b"
DEFAULT_FREEZE_SECONDS = 3600
PROJECT_ENV_PATH = Path(__file__).resolve().parents[2] / ".viewer.env"
_API_KEY_ENV_NAMES = (
    "VIEWER_SUPER_DISPATCH_API_KEY",
    "VIEWER_OPENAI_API_KEY",
    "OPENAI_API_KEY",
)


class LLMChainError(RuntimeError):
    """Raised when every provider in the chain failed or was unavailable."""

    def __init__(self, message: str, attempts: list[dict[str, Any]] | None = None):
        super().__init__(message)
        self.attempts = attempts or []


@dataclass
class LLMResult:
    content: str
    payload: dict[str, Any]
    provider_id: str
    provider_name: str
    model: str
    latency_s: float
    attempts: list[dict[str, Any]] = field(default_factory=list)


def _health_path() -> Path:
    return DATA_DIR / "llm-provider-health.json"


def _load_health() -> dict[str, dict[str, Any]]:
    path = _health_path()
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except Exception:
        return {}
    if not isinstance(raw, dict):
        return {}
    states: dict[str, dict[str, Any]] = {}
    for key, value in raw.items():
        if isinstance(key, str) and isinstance(value, dict):
            states[key] = value
    return states


def _save_health(states: dict[str, dict[str, Any]]) -> None:
    path = _health_path()
    now = time.time()
    pruned = {
        key: value
        for key, value in states.items()
        if float(value.get("frozen_until") or 0) > now
    }
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        tmp = path.with_suffix(".tmp")
        tmp.write_text(json.dumps(pruned, indent=2), encoding="utf-8")
        tmp.replace(path)
    except OSError:
        pass


def freeze_provider(provider_id: str, error: str, freeze_seconds: int) -> None:
    if not provider_id or freeze_seconds <= 0:
        return
    states = _load_health()
    previous = states.get(provider_id) or {}
    states[provider_id] = {
        "frozen_until": time.time() + freeze_seconds,
        "error": error[:500],
        "failures": int(previous.get("failures") or 0) + 1,
    }
    _save_health(states)


def clear_provider_state(provider_id: str | None = None) -> None:
    if provider_id is None:
        _save_health({})
        return
    states = _load_health()
    states.pop(provider_id, None)
    _save_health(states)


def provider_states() -> dict[str, Any]:
    """Return per-provider freeze state for the Settings UI."""
    now = time.time()
    states: dict[str, Any] = {}
    for provider_id, value in _load_health().items():
        frozen_until = float(value.get("frozen_until") or 0)
        frozen = frozen_until > now
        states[provider_id] = {
            "frozen": frozen,
            "frozen_until": frozen_until if frozen else None,
            "freeze_seconds_remaining": max(0, int(frozen_until - now)) if frozen else 0,
            "error": str(value.get("error") or ""),
            "failures": int(value.get("failures") or 0),
        }
    return states


def _read_env_value(path: Path, key: str) -> str:
    try:
        for line in path.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            name, _, value = line.partition("=")
            if name.strip() == key:
                return value.strip().strip('"').strip("'")
    except OSError:
        pass
    return ""


def _resolve_api_key(profile: dict[str, Any]) -> str:
    profile_key = str(profile.get("api_key") or "").strip()
    if profile_key:
        return profile_key
    for name in _API_KEY_ENV_NAMES:
        value = os.environ.get(name, "").strip()
        if value:
            return value
    env_paths: list[Path] = []
    configured = os.environ.get("VIEWER_PROJECT_ENV_PATH", "").strip()
    if configured:
        env_paths.append(Path(configured).expanduser())
    env_paths.append(PROJECT_ENV_PATH)
    for env_path in env_paths:
        for name in _API_KEY_ENV_NAMES:
            value = _read_env_value(env_path, name)
            if value:
                return value
    return ""


def _models_url(chat_completions_url: str) -> str:
    base = chat_completions_url.rstrip("/")
    suffix = "/chat/completions"
    if base.endswith(suffix):
        return f"{base[:-len(suffix)]}/models"
    if "/v1" in base:
        return f"{base.rsplit('/v1', 1)[0]}/v1/models"
    return f"{base}/models"


def _discover_model(chat_completions_url: str, api_key: str) -> str:
    request = Request(
        _models_url(chat_completions_url),
        headers={"Authorization": f"Bearer {api_key or 'EMPTY'}", "Content-Type": "application/json"},
        method="GET",
    )
    try:
        with urlopen(request, timeout=5) as response:
            payload = json.loads(response.read().decode("utf-8"))
    except (OSError, URLError, HTTPError, json.JSONDecodeError):
        return ""
    data = payload.get("data") if isinstance(payload, dict) else None
    if not isinstance(data, list):
        return ""
    for item in data:
        if isinstance(item, dict) and isinstance(item.get("id"), str) and item["id"].strip():
            return item["id"].strip()
    return ""


def _env_fallback_chain() -> tuple[list[dict[str, Any]], int]:
    return (
        [
            {
                "id": "env",
                "name": "Environment",
                "api_url": os.environ.get("VIEWER_SUPER_DISPATCH_URL", DEFAULT_LLM_URL),
                "model": os.environ.get("VIEWER_SUPER_DISPATCH_MODEL", DEFAULT_LLM_MODEL),
                "api_key": "",
                "enabled": True,
            }
        ],
        DEFAULT_FREEZE_SECONDS,
    )


def load_llm_chain() -> tuple[list[dict[str, Any]], int]:
    """Return the ordered provider chain and freeze window from Viewer config."""
    try:
        from .files import read_config

        config = read_config().super_workspace
    except Exception:
        return _env_fallback_chain()
    profiles = [profile.model_dump() for profile in config.dispatch_profiles]
    freeze_seconds = int(getattr(config, "llm_provider_freeze_seconds", DEFAULT_FREEZE_SECONDS) or 0)
    if not profiles:
        return _env_fallback_chain()
    return profiles, max(0, freeze_seconds)


def chat_completion(
    messages: list[dict[str, Any]],
    *,
    response_format: dict[str, Any] | None = None,
    timeout: float = 30,
    max_tokens: int | None = None,
    temperature: float | None = None,
    extra_payload: dict[str, Any] | None = None,
) -> LLMResult:
    """Call the first healthy provider in the ordered chain.

    Failed providers are frozen for the configured window and the next
    provider is tried. Raises :class:`LLMChainError` when every provider
    fails or is frozen/disabled.
    """
    profiles, freeze_seconds = load_llm_chain()
    states = _load_health()
    now = time.time()
    attempts: list[dict[str, Any]] = []
    last_error = "no providers configured"

    for profile in profiles:
        provider_id = str(profile.get("id") or "").strip()
        provider_name = str(profile.get("name") or provider_id or "provider")
        if not provider_id:
            continue
        if not profile.get("enabled", True):
            attempts.append({"provider_id": provider_id, "name": provider_name, "skipped": "disabled"})
            continue
        state = states.get(provider_id) or {}
        frozen_until = float(state.get("frozen_until") or 0)
        if frozen_until > now:
            attempts.append(
                {
                    "provider_id": provider_id,
                    "name": provider_name,
                    "skipped": "frozen",
                    "freeze_seconds_remaining": int(frozen_until - now),
                }
            )
            continue

        url = str(profile.get("api_url") or "").strip() or DEFAULT_LLM_URL
        api_key = _resolve_api_key(profile)
        model = str(profile.get("model") or "").strip()
        if not model:
            model = _discover_model(url, api_key) or "default"

        payload: dict[str, Any] = {"model": model, "messages": messages}
        if response_format is not None:
            payload["response_format"] = response_format
        if max_tokens is not None:
            payload["max_tokens"] = max_tokens
        if temperature is not None:
            payload["temperature"] = temperature
        if extra_payload:
            payload.update(extra_payload)

        request = Request(
            url,
            data=json.dumps(payload).encode("utf-8"),
            headers={"Authorization": f"Bearer {api_key or 'EMPTY'}", "Content-Type": "application/json"},
            method="POST",
        )
        started = time.time()
        try:
            with urlopen(request, timeout=timeout) as response:
                response_payload = json.loads(response.read().decode("utf-8"))
            content = (
                response_payload.get("choices", [{}])[0]
                .get("message", {})
                .get("content", "")
            )
            if not isinstance(content, str) or not content.strip():
                raise ValueError("empty completion content")
        except HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")[:500]
            last_error = f"{provider_name}: HTTP {exc.code}: {detail}"
        except (OSError, URLError, json.JSONDecodeError, ValueError) as exc:
            last_error = f"{provider_name}: {exc}"
        else:
            return LLMResult(
                content=content,
                payload=response_payload,
                provider_id=provider_id,
                provider_name=provider_name,
                model=model,
                latency_s=time.time() - started,
                attempts=attempts,
            )
        freeze_provider(provider_id, last_error, freeze_seconds)
        attempts.append({"provider_id": provider_id, "name": provider_name, "error": last_error})

    raise LLMChainError(f"All LLM providers failed or are frozen: {last_error}", attempts)
