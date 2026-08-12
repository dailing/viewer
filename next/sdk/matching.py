"""Channel pattern matching (protocol spec section 4.2).

Duplicated from the kernel on purpose: the SDK must stay extractable as a
standalone package (`viewer-plugin-sdk`) without importing kernel internals.
"""

from __future__ import annotations


def match(pattern: str, channel: str) -> bool:
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
