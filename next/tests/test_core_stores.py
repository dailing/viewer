"""C1 config-store / C2 instance-store / C3 file-service tests against a real kernel."""

from __future__ import annotations

import base64
import hashlib

import pytest

from kernel.server import KernelServer
from plugins.configstore import ConfigStorePlugin
from plugins.fileservice import FileServicePlugin
from plugins.instancestore import InstanceStorePlugin
from sdk import BusClient, RpcError


def url(port: int) -> str:
    return f"ws://127.0.0.1:{port}/ws"


CALLER = {"id": "caller", "version": "0", "slots": {}, "emits": {}}


@pytest.fixture
async def caller(kernel: KernelServer):
    client = BusClient(url(kernel.port), CALLER)
    await client.connect()
    try:
        yield client
    finally:
        await client.close()


# ------------------------------------------------------------------ C1 config


@pytest.mark.asyncio
async def test_config_set_get_mailbox_and_reload(
    kernel: KernelServer, caller: BusClient, tmp_path
) -> None:
    db = tmp_path / "config.json"
    store = ConfigStorePlugin(db_path=db)
    await store.start(url(kernel.port))
    await caller.request("config:_:set", {"plugin": "chat", "key": "roles", "value": ["a", "b"]})
    await caller.request("config:_:set", {"plugin": "chat", "key": "cwd", "value": "/tmp"})

    assert await caller.request("config:_:get", {"plugin": "chat", "key": "roles"}) == ["a", "b"]
    assert await caller.request("config:_:get", {"plugin": "chat"}) == {
        "roles": ["a", "b"],
        "cwd": "/tmp",
    }
    # State channel: full current config, atomically replayed to subscribers.
    retained = await kernel.broker.get_retained("config:chat:config")
    assert retained["value"] == {"roles": ["a", "b"], "cwd": "/tmp"}
    # value=None deletes a key.
    await caller.request("config:_:set", {"plugin": "chat", "key": "cwd", "value": None})
    assert await caller.request("config:_:get", {"plugin": "chat"}) == {"roles": ["a", "b"]}
    await store.stop()

    # Persistence: a fresh instance on the same db file sees the config.
    store2 = ConfigStorePlugin(db_path=db)
    await store2.start(url(kernel.port))
    assert await caller.request("config:_:get", {"plugin": "chat"}) == {"roles": ["a", "b"]}
    await store2.stop()


@pytest.mark.asyncio
async def test_config_validation_errors(kernel: KernelServer, caller: BusClient, tmp_path) -> None:
    store = ConfigStorePlugin(db_path=tmp_path / "config.json")
    await store.start(url(kernel.port))
    with pytest.raises(RpcError, match="invalid_request"):
        await caller.request("config:_:get", {})
    with pytest.raises(RpcError, match="invalid_request"):
        await caller.request("config:_:set", {"plugin": "chat"})
    assert await caller.request("config:_:get", {"plugin": "ghost"}) == {}
    await store.stop()


# ----------------------------------------------------------------- C2 instance


@pytest.mark.asyncio
async def test_instance_crud_mailbox_and_tombstone(
    kernel: KernelServer, caller: BusClient, tmp_path
) -> None:
    store = InstanceStorePlugin(db_path=tmp_path / "instances.json")
    await store.start(url(kernel.port))

    await caller.request(
        "instance:_:set",
        {"plugin": "chat", "instance": "42", "key": "cwd", "value": "/repo"},
    )
    result = await caller.request(
        "instance:_:set",
        {"plugin": "chat", "instance": "42", "key": "roles", "value": ["x"]},
    )
    assert result["state"] == {"cwd": "/repo", "roles": ["x"]}
    retained = await kernel.broker.get_retained("instance-store:chat:42")
    assert retained["value"] == {"cwd": "/repo", "roles": ["x"]}

    assert await caller.request(
        "instance:_:get", {"plugin": "chat", "instance": "42", "key": "cwd"}
    ) == "/repo"
    # Whole-state replace (key omitted).
    await caller.request(
        "instance:_:set",
        {"plugin": "chat", "instance": "42", "value": {"fresh": True}},
    )
    assert await caller.request(
        "instance:_:get", {"plugin": "chat", "instance": "42"}
    ) == {"fresh": True}
    assert await caller.request("instance:_:list", {"plugin": "chat"}) == {"42": {"fresh": True}}

    deleted = await caller.request(
        "instance:_:delete", {"plugin": "chat", "instance": "42"}
    )
    assert deleted["existed"] is True
    assert await caller.request("instance:_:get", {"plugin": "chat", "instance": "42"}) is None
    tombstone = await kernel.broker.get_retained("instance-store:chat:42")
    assert tombstone["value"] is None
    await store.stop()


# -------------------------------------------------------------------- C3 file


@pytest.mark.asyncio
async def test_file_resolve_read_hash(kernel: KernelServer, caller: BusClient, tmp_path) -> None:
    service = FileServicePlugin()
    await service.start(url(kernel.port))
    text_file = tmp_path / "note.txt"
    text_file.write_text("héllo viewer")
    binary_file = tmp_path / "blob.bin"
    binary_file.write_bytes(b"\x00\x01\x02\xff" * 64)

    resolved = await caller.request("file:_:resolve", {"path": str(text_file)})
    assert resolved["size"] == len("héllo viewer".encode())
    assert resolved["sha256"] == hashlib.sha256("héllo viewer".encode()).hexdigest()

    read = await caller.request("file:_:read", {"path": str(text_file)})
    assert read == {
        "path": str(text_file),
        "size": len("héllo viewer".encode()),
        "encoding": "utf-8",
        "content": "héllo viewer",
    }
    binary = await caller.request("file:_:read", {"path": str(binary_file)})
    assert binary["encoding"] == "base64"
    assert base64.b64decode(binary["content"]) == b"\x00\x01\x02\xff" * 64

    hashed = await caller.request("file:_:hash", {"path": str(binary_file)})
    assert hashed["sha256"] == hashlib.sha256(b"\x00\x01\x02\xff" * 64).hexdigest()

    with pytest.raises(RpcError, match="too_large"):
        await caller.request("file:_:read", {"path": str(text_file), "max_bytes": 2})
    with pytest.raises(RpcError, match="not_found"):
        await caller.request("file:_:resolve", {"path": str(tmp_path / "ghost.txt")})
    await service.stop()
