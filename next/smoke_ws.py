"""Real-process smoke test: double client, atomic handoff, SIGINT -> 4009."""

from __future__ import annotations

import asyncio
import json
import signal
import subprocess
import sys
import uuid

import websockets

PORT = 8971
URL = f"ws://127.0.0.1:{PORT}/ws"


def hello(plugin: str) -> dict:
    return {
        "type": "hello",
        "protocol_version": 1,
        "conn": str(uuid.uuid4()),
        "manifest": {"id": plugin, "version": "0.1.0", "slots": {}, "emits": {}},
        "managed": False,
    }


async def main() -> int:
    proc = subprocess.Popen(
        [sys.executable, "-m", "kernel", "--port", str(PORT)],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    try:
        for _ in range(50):
            await asyncio.sleep(0.1)
            if proc.poll() is not None:
                print("SMOKE-FAIL: kernel exited early")
                return 1
            try:
                async with websockets.connect(URL) as probe:
                    await probe.send(json.dumps(hello("probe")))
                    break
            except OSError:
                continue

        async with websockets.connect(URL) as producer:
            await producer.send(json.dumps(hello("producer")))
            await producer.send(json.dumps(
                {"type": "set", "channel": "smoke:_:state", "value": {"v": 1}}
            ))
            await asyncio.sleep(0.2)

            async with websockets.connect(URL) as subscriber:
                await subscriber.send(json.dumps(hello("subscriber")))
                await subscriber.send(json.dumps(
                    {"type": "subscribe", "pattern": "smoke:_:state"}
                ))
                retained = json.loads(await asyncio.wait_for(subscriber.recv(), 2))
                assert retained["channel"] == "smoke:_:state", retained
                assert retained["value"] == {"v": 1}, retained
                assert retained["origin"]["plugin"] == "producer", retained
                print("SMOKE: retained handoff OK", retained["channel"], retained["value"])

                await producer.send(json.dumps(
                    {"type": "set", "channel": "smoke:_:state", "value": {"v": 2}}
                ))
                live = json.loads(await asyncio.wait_for(subscriber.recv(), 2))
                assert live["value"] == {"v": 2}, live
                print("SMOKE: live replace OK", live["value"])

            proc.send_signal(signal.SIGINT)
            try:
                closed = await asyncio.wait_for(producer.recv(), 5)
                print("SMOKE-FAIL: unexpected frame", closed)
                return 1
            except websockets.exceptions.ConnectionClosed as exc:
                code = exc.rcvd.code if exc.rcvd else None
                assert code == 4009, f"close code {code}"
                print("SMOKE: SIGINT close 4009 OK")

        proc.wait(timeout=5)
        assert proc.returncode == 0, f"kernel exit {proc.returncode}"
        print("SMOKE-PASS")
        return 0
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait()


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
