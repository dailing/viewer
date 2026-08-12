from __future__ import annotations

import pytest

from kernel.protocol import ProtocolError, channel_matches, validate_channel, validate_pattern


@pytest.mark.parametrize(
    ("pattern", "channel", "expected"),
    [
        ("chat:42:status", "chat:42:status", True),
        ("*:42:status", "chat:42:status", True),
        ("chat:*:status", "chat:42:status", True),
        ("chat:42:*", "chat:42:status", True),
        ("chat:42:status:*", "chat:42:status:detail", True),
        ("chat:42", "chat:42:message", True),
        ("chat", "chat:42:message:detail", True),
        ("chat:42:message:detail", "chat:42:message", False),
        (">", "_inbox:c1:r1", True),
        ("chat:*:status", "chat:42:message", False),
    ],
)
def test_matching_table(pattern: str, channel: str, expected: bool) -> None:
    assert channel_matches(pattern, channel) is expected


@pytest.mark.parametrize(
    "channel",
    ["Chat:42:status", "chat::status", "-chat:42:status", "chat:_bad:status", "chat:42"],
)
def test_invalid_channels(channel: str) -> None:
    with pytest.raises(ProtocolError):
        validate_channel(channel)


@pytest.mark.parametrize(
    "pattern", ["Chat:*", "chat::status", "-chat:*", "chat:_bad", "chat:x*", "chat:>", ">:x"]
)
def test_invalid_patterns(pattern: str) -> None:
    with pytest.raises(ProtocolError):
        validate_pattern(pattern)


def test_reserved_namespaces_are_grammatical() -> None:
    assert validate_channel("_inbox:c1:r1") == "_inbox:c1:r1"
    assert validate_channel("plugins:_:list") == "plugins:_:list"
