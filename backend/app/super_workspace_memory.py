from __future__ import annotations

import json
import os
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from loguru import logger


HINDSIGHT_CODEX_CONFIG = Path.home() / ".hindsight" / "codex.json"
HINDSIGHT_RETAIN_TIMEOUT_SECONDS = 3


def _read_hindsight_codex_config() -> dict[str, Any]:
    try:
        raw = json.loads(HINDSIGHT_CODEX_CONFIG.read_text(encoding="utf-8"))
        return raw if isinstance(raw, dict) else {}
    except (OSError, json.JSONDecodeError):
        return {}


def _api_url(config_url: str) -> str:
    value = os.environ.get("VIEWER_HINDSIGHT_API_URL", "").strip() or config_url.strip()
    if not value:
        value = str(_read_hindsight_codex_config().get("hindsightApiUrl") or "").strip()
    return value.rstrip("/")


def _api_token() -> str | None:
    token = os.environ.get("VIEWER_HINDSIGHT_API_TOKEN", "").strip()
    if token:
        return token
    value = _read_hindsight_codex_config().get("hindsightApiToken")
    return value.strip() if isinstance(value, str) and value.strip() else None


def chat_memory_bank_id(prefix: str, user_id: str, workspace_id: str, chat_id: str) -> str:
    cleaned_prefix = (prefix or "super-workspace").strip() or "super-workspace"
    return f"{cleaned_prefix}::{user_id}::{workspace_id}::chat::{chat_id}"


def _request_json(method: str, url: str, body: dict[str, Any], token: str | None, timeout: int) -> dict[str, Any]:
    headers = {
        "Content-Type": "application/json",
        "User-Agent": "viewer-super-workspace-memory/1",
    }
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = urllib.request.Request(url, data=json.dumps(body).encode("utf-8"), headers=headers, method=method)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        raw = response.read().decode("utf-8")
        return json.loads(raw) if raw else {}


def _iso_timestamp(value: float | None) -> str:
    timestamp = value if isinstance(value, (int, float)) and value > 0 else time.time()
    return datetime.fromtimestamp(timestamp, tz=timezone.utc).isoformat()


def retain_visible_message(
    *,
    user_id: str,
    workspace_id: str | None,
    chat_id: str | None,
    message_id: str,
    role: str,
    text: str,
    occurred_at: float | None = None,
    provider: str = "",
    event_type: str = "",
    role_id: str | None = None,
    sender_role_id: str | None = None,
    recipient_role_id: str | None = None,
) -> None:
    content = text.strip()
    if not content or not workspace_id or not chat_id:
        return

    try:
        from .files import read_config

        memory_config = read_config().super_workspace
        if not memory_config.hindsight_retain_enabled:
            return
        api_url = _api_url(memory_config.hindsight_api_url)
        if not api_url:
            return
        bank_id = chat_memory_bank_id(memory_config.hindsight_bank_prefix, user_id, workspace_id, chat_id)
        url = f"{api_url}/v1/default/banks/{urllib.parse.quote(bank_id, safe='')}/memories"
        timestamp = _iso_timestamp(occurred_at)
        document_id = f"super-workspace-message:{message_id}"
        payload = {
            "items": [
                {
                    "content": (
                        "Super Workspace visible chat message\n"
                        f"Role: {role}\n"
                        f"Provider: {provider or 'viewer'}\n"
                        f"Event type: {event_type}\n"
                        f"Occurred at: {timestamp}\n\n"
                        f"{content}"
                    ),
                    "document_id": document_id,
                    "context": "super-workspace-chat",
                    "metadata": {
                        "user_id": user_id,
                        "workspace_id": workspace_id,
                        "chat_id": chat_id,
                        "message_id": message_id,
                        "role": role,
                        "role_id": role_id or "",
                        "sender_role_id": sender_role_id or "",
                        "recipient_role_id": recipient_role_id or "",
                        "provider": provider,
                        "event_type": event_type,
                        "occurred_at": timestamp,
                    },
                    "tags": [
                        "super-workspace",
                        f"user:{user_id}",
                        f"workspace:{workspace_id}",
                        f"chat:{chat_id}",
                    ],
                }
            ],
            "async": True,
        }
        _request_json("POST", url, payload, _api_token(), HINDSIGHT_RETAIN_TIMEOUT_SECONDS)
        logger.debug("Retained Super Workspace visible message to Hindsight bank={} message_id={}", bank_id, message_id)
    except (OSError, urllib.error.URLError, RuntimeError, ValueError, json.JSONDecodeError) as exc:
        logger.debug("Skipping Hindsight retain for Super Workspace message_id={} reason={}", message_id, exc)
    except Exception as exc:
        logger.warning("Unexpected Hindsight retain failure message_id={} reason={}", message_id, exc)


def retain_visible_message_background(**kwargs: Any) -> None:
    thread = threading.Thread(target=retain_visible_message, kwargs=kwargs, daemon=True)
    thread.start()
