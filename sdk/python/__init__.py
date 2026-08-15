"""Viewer plugin SDK (Python half of framework section 14.2)."""

from .client import BusClient, RpcError
from .matching import match
from .plugin import Ctx, Plugin, slot

__all__ = ["BusClient", "Ctx", "Plugin", "RpcError", "match", "slot"]
