"""Wire-level validation and channel matching for the Viewer bus."""

from __future__ import annotations

import re
import uuid
from collections.abc import Mapping
from typing import Any, TypeGuard

PROTOCOL_VERSION = 1
MAX_DEPTH = 8
DEFAULT_FRAME_SIZE = 1024 * 1024
DEFAULT_OUTBOUND_QUEUE = 1000

FRAME_TYPES = frozenset({"hello", "publish", "set", "subscribe", "unsubscribe"})
_NORMAL_FIELD = re.compile(r"[a-z0-9][a-z0-9_-]*\Z")
_RESERVED_FIELD = re.compile(r"_[a-z0-9_-]*\Z")


class ProtocolError(ValueError):
    """A non-fatal wire protocol validation error."""

    def __init__(self, code: str, message: str, detail: Any | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.message = message
        self.detail = detail


class HelloValidationError(ValueError):
    """A fatal hello schema error."""


def _is_object(value: object) -> TypeGuard[dict[str, Any]]:
    return isinstance(value, dict) and all(isinstance(key, str) for key in value)


def _valid_field(field: str, *, first: bool) -> bool:
    if _NORMAL_FIELD.fullmatch(field):
        return True
    if field == "_":
        return True
    return first and _RESERVED_FIELD.fullmatch(field) is not None


def validate_channel(channel: object) -> str:
    """Validate a concrete channel and return it.

    Concrete messages have the protocol's fixed three semantic fields. Reserved
    fields are ``_`` placeholders, or an underscore-prefixed first field such as
    ``_inbox`` and ``_conn``.
    """

    if not isinstance(channel, str):
        raise ProtocolError("invalid_channel", "channel must be a string")
    fields = channel.split(":")
    if len(fields) < 3:
        raise ProtocolError("invalid_channel", "channel must contain at least three fields")
    if any(not _valid_field(field, first=index == 0) for index, field in enumerate(fields)):
        raise ProtocolError("invalid_channel", f"invalid channel: {channel}")
    return channel


def validate_pattern(pattern: object) -> str:
    """Validate a subscription pattern and return it."""

    if not isinstance(pattern, str):
        raise ProtocolError("invalid_pattern", "pattern must be a string")
    if pattern == ">":
        return pattern
    fields = pattern.split(":")
    if not fields or any(not field for field in fields):
        raise ProtocolError("invalid_pattern", f"invalid pattern: {pattern}")
    for index, field in enumerate(fields):
        if field == "*":
            continue
        if ">" in field or "*" in field or not _valid_field(field, first=index == 0):
            raise ProtocolError("invalid_pattern", f"invalid pattern: {pattern}")
    return pattern


def channel_matches(pattern: str, channel: str) -> bool:
    """Implement protocol specification section 4.2 exactly."""

    validate_pattern(pattern)
    validate_channel(channel)
    if pattern == ">":
        return True
    pattern_fields = pattern.split(":")
    channel_fields = channel.split(":")
    if len(pattern_fields) > len(channel_fields):
        return False
    return all(
        pattern_field == "*" or pattern_field == channel_fields[index]
        for index, pattern_field in enumerate(pattern_fields)
    )


def validate_hello(frame: object) -> dict[str, Any]:
    """Strongly validate the first-frame hello schema.

    Version compatibility is deliberately checked by the server so it can use
    the distinct 4003 close code.
    """

    if not _is_object(frame):
        raise HelloValidationError("hello must be a JSON object")
    required = {"type", "protocol_version", "conn", "manifest", "managed"}
    missing = sorted(required - frame.keys())
    if missing:
        raise HelloValidationError(f"hello missing required fields: {', '.join(missing)}")
    if frame["type"] != "hello":
        raise HelloValidationError("frame type must be hello")
    if isinstance(frame["protocol_version"], bool) or not isinstance(frame["protocol_version"], int):
        raise HelloValidationError("protocol_version must be an integer")
    conn = frame["conn"]
    if not isinstance(conn, str):
        raise HelloValidationError("conn must be a UUIDv4 string")
    try:
        parsed_conn = uuid.UUID(conn)
    except (ValueError, AttributeError) as exc:
        raise HelloValidationError("conn must be a UUIDv4 string") from exc
    if parsed_conn.version != 4:
        raise HelloValidationError("conn must be a UUIDv4 string")
    manifest = frame["manifest"]
    if not _is_object(manifest):
        raise HelloValidationError("manifest must be an object")
    manifest_required = {"id", "version", "slots", "emits"}
    manifest_missing = sorted(manifest_required - manifest.keys())
    if manifest_missing:
        raise HelloValidationError(
            f"manifest missing required fields: {', '.join(manifest_missing)}"
        )
    plugin_id = manifest["id"]
    if not isinstance(plugin_id, str) or _NORMAL_FIELD.fullmatch(plugin_id) is None:
        raise HelloValidationError("manifest.id must be a valid non-reserved channel field")
    if not isinstance(manifest["version"], str) or not manifest["version"]:
        raise HelloValidationError("manifest.version must be a non-empty string")
    if not _is_object(manifest["slots"]) or not _is_object(manifest["emits"]):
        raise HelloValidationError("manifest.slots and manifest.emits must be objects")
    if not isinstance(frame["managed"], bool):
        raise HelloValidationError("managed must be a boolean")
    if "instance_id" in frame:
        instance_id = frame["instance_id"]
        if not isinstance(instance_id, str) or not _valid_field(instance_id, first=False):
            raise HelloValidationError("instance_id must be a valid channel field")
    return frame


def validate_post_hello_frame(frame: object) -> dict[str, Any]:
    """Validate protocol metadata while deliberately leaving ``value`` free-form."""

    if not _is_object(frame):
        raise ProtocolError("malformed_frame", "frame must be a JSON object")
    frame_type = frame.get("type")
    if not isinstance(frame_type, str):
        raise ProtocolError("malformed_frame", "frame.type must be a string")
    if frame_type not in FRAME_TYPES:
        raise ProtocolError("unknown_type", f"unknown frame type: {frame_type}")
    if frame_type == "hello":
        raise ProtocolError("unexpected_hello", "hello may only be sent as the first frame")
    if frame_type in {"subscribe", "unsubscribe"}:
        validate_pattern(frame.get("pattern"))
        return frame
    validate_channel(frame.get("channel"))
    if "value" not in frame:
        raise ProtocolError("malformed_frame", f"{frame_type} frame missing value")
    trace_id = frame.get("trace_id")
    if trace_id is not None and (not isinstance(trace_id, str) or not trace_id):
        raise ProtocolError("malformed_frame", "trace_id must be a non-empty string")
    depth = frame.get("depth", 0)
    if isinstance(depth, bool) or not isinstance(depth, int) or depth < 0:
        raise ProtocolError("malformed_frame", "depth must be a non-negative integer")
    return frame


def frame_limit_for(
    frame: Mapping[str, Any] | None,
    overrides: Mapping[str, int],
    default: int = DEFAULT_FRAME_SIZE,
) -> int:
    """Return the largest configured limit matching a frame's channel prefix."""

    if frame is None or not isinstance(frame.get("channel"), str):
        return default
    channel = frame["channel"]
    limits = [limit for prefix, limit in overrides.items() if channel.startswith(prefix)]
    return max([default, *limits])
