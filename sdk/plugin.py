"""Compatibility shim: `from sdk.plugin import ...` resolves here (the real
implementation lives in sdk/python/plugin.py)."""

from sdk.python.plugin import Ctx, Plugin, slot  # noqa: F401
