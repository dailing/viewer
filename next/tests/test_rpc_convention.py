from __future__ import annotations

import asyncio
import json
import uuid

import pytest

from conftest import open_client, receive_channel
from kernel.server import KernelServer


@pytest.mark.asyncio
async def test_rpc_is_open_publish_subscribe_traffic(kernel: KernelServer) -> None:
    caller, caller_hello = await open_client(kernel, "caller")
    callee, _ = await open_client(kernel, "callee")
    observer, _ = await open_client(kernel, "observer")
    corr = uuid.uuid4().hex
    inbox = f"_inbox:{caller_hello['conn']}:{corr}"
    await caller.send(json.dumps({"type": "subscribe", "pattern": inbox}))
    await callee.send(json.dumps({"type": "subscribe", "pattern": "service:_:get"}))
    await observer.send(json.dumps({"type": "subscribe", "pattern": ">"}))
    await asyncio.sleep(0.05)
    request = {"_reply_to": inbox, "_corr": corr, "key": "theme"}
    await caller.send(json.dumps({"type": "publish", "channel": "service:_:get", "value": request}))
    incoming = await receive_channel(callee, "service:_:get")
    assert incoming["value"]["_corr"] == corr
    seen_request = await receive_channel(observer, "service:_:get")
    assert seen_request["value"]["_reply_to"] == inbox
    await callee.send(json.dumps({
        "type": "publish", "channel": incoming["value"]["_reply_to"],
        "value": {"_corr": corr, "ok": True, "result": {"theme": "dark"}},
    }))
    response = await receive_channel(caller, inbox)
    assert response["value"]["_corr"] == corr
    assert response["value"]["result"] == {"theme": "dark"}
    assert (await receive_channel(observer, inbox))["value"]["_corr"] == corr
    await asyncio.gather(caller.close(), callee.close(), observer.close())
