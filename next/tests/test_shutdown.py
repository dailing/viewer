from __future__ import annotations

import pytest
from websockets.exceptions import ConnectionClosed

from conftest import open_client
from kernel.server import KernelServer, ServerConfig


@pytest.mark.asyncio
async def test_graceful_shutdown_close_code() -> None:
    server = KernelServer(ServerConfig(port=0))
    await server.start()
    client, _ = await open_client(server, "client")
    await server.stop()
    with pytest.raises(ConnectionClosed) as raised:
        await client.recv()
    assert raised.value.code == 4009
