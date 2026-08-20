#!/usr/bin/env python3
"""Black-box smoke checks for the Go file-service."""

from __future__ import annotations

import argparse
import asyncio
import base64
import hashlib
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from sdk import BusClient, RpcError  # noqa: E402


def manifest(plugin_id: str) -> dict[str, Any]:
    return {"id": plugin_id, "version": "1.0.0", "slots": {}, "emits": {}}


async def expect_error(client: BusClient, channel: str, value: Any, code: str) -> None:
    try:
        await client.request(channel, value, timeout=2)
    except RpcError as exc:
        assert exc.code == code, (channel, exc.code, exc.message)
    else:
        raise AssertionError(f"{channel} unexpectedly succeeded")


async def wait_for_service(client: BusClient, process: subprocess.Popen[bytes]) -> None:
    deadline = asyncio.get_running_loop().time() + 5
    while True:
        if process.poll() is not None:
            raise AssertionError(f"file-service exited with {process.returncode}")
        try:
            await client.request("file:_:resolve", {}, timeout=0.2)
        except RpcError as exc:
            if exc.code == "invalid_request":
                return
        except TimeoutError:
            pass
        if asyncio.get_running_loop().time() >= deadline:
            raise TimeoutError("file-service did not become ready")
        await asyncio.sleep(0.05)


async def run(args: argparse.Namespace) -> None:
    with tempfile.TemporaryDirectory(prefix="viewer-files-") as temporary:
        temp = Path(temporary)
        home = temp / "home"
        home.mkdir()
        text_path = temp / "hello.txt"
        text_path.write_text("héllo\n")
        binary_path = temp / "binary.dat"
        binary_path.write_bytes(b"\xff\x00\x80")
        large_path = temp / "large.dat"
        large_path.write_bytes(b"x" * (1024 * 1024 + 1))
        adjustable_path = temp / "adjustable.dat"
        adjustable_path.write_bytes(b"y" * 4096)
        home_path = home / "home.txt"
        home_path.write_text("expanded")
        listing_path = temp / "listing"
        (listing_path / "A-dir" / "level-two").mkdir(parents=True)
        (listing_path / "z-dir").mkdir()
        (listing_path / "A-dir" / "level-two" / "deep.txt").write_text("deep")
        (listing_path / "a.txt").write_text("alpha")
        (listing_path / "B.json").write_text("{}")
        (listing_path / ".hidden").write_text("secret")
        (listing_path / "A-dir" / ".nested-hidden").write_text("secret")
        (listing_path / "link.json").symlink_to(listing_path / "B.json")
        log_path = temp / "fileservice.log"
        environment = os.environ.copy()
        environment["HOME"] = str(home)
        with log_path.open("wb") as log:
            process = subprocess.Popen(
                [args.fileservice_bin, "--kernel-ws", args.kernel_ws],
                stdout=log,
                stderr=log,
                env=environment,
            )
        client = BusClient(args.kernel_ws, manifest("smoke-fileservice"), reconnect=False)
        await client.connect()
        await client.wait_registered()
        try:
            await wait_for_service(client, process)
            await expect_error(client, "file:_:resolve", {}, "invalid_request")
            await expect_error(client, "file:_:resolve", {"path": str(temp / "missing")}, "not_found")
            resolved = await client.request("file:_:resolve", {"path": str(text_path)})
            assert resolved == {
                "path": str(text_path.resolve()),
                "exists": True,
                "is_dir": False,
                "size": len(text_path.read_bytes()),
                "mtime": int(text_path.stat().st_mtime),
                "sha256": hashlib.sha256(text_path.read_bytes()).hexdigest(),
            }
            directory = await client.request("file:_:resolve", {"path": str(temp)})
            assert directory["is_dir"] is True and directory["sha256"] is None
            expanded = await client.request("file:_:resolve", {"path": "~/home.txt"})
            assert expanded["path"] == str(home_path.resolve())
            print("file resolve/stat/hash/path expansion/errors: PASS")

            await expect_error(client, "file:_:read", {}, "invalid_request")
            await expect_error(client, "file:_:read", {"path": str(temp / "missing")}, "not_found")
            await expect_error(client, "file:_:read", {"path": str(temp)}, "read_error")
            await expect_error(
                client, "file:_:read", {"path": str(large_path)}, "too_large"
            )
            await expect_error(
                client,
                "file:_:read",
                {"path": str(text_path), "max_bytes": 1},
                "too_large",
            )
            text = await client.request("file:_:read", {"path": str(text_path)})
            assert text["encoding"] == "utf-8" and text["content"] == "héllo\n"
            binary = await client.request("file:_:read", {"path": str(binary_path)})
            assert binary["encoding"] == "base64"
            assert binary["content"] == base64.b64encode(binary_path.read_bytes()).decode("ascii")
            adjustable = await client.request(
                "file:_:read",
                {"path": str(adjustable_path), "max_bytes": adjustable_path.stat().st_size},
            )
            assert adjustable["size"] == adjustable_path.stat().st_size
            print("file read utf-8/base64/limit/not-found/read-error: PASS")

            await expect_error(client, "file:_:hash", {}, "invalid_request")
            await expect_error(client, "file:_:hash", {"path": str(temp / "missing")}, "not_found")
            await expect_error(client, "file:_:hash", {"path": str(temp)}, "not_found")
            hashed = await client.request("file:_:hash", {"path": str(binary_path)})
            assert hashed == {
                "path": str(binary_path.resolve()),
                "sha256": hashlib.sha256(binary_path.read_bytes()).hexdigest(),
            }
            print("file hash success/errors: PASS")

            await expect_error(client, "file:_:list", {}, "invalid_request")
            await expect_error(
                client, "file:_:list", {"path": str(temp / "missing")}, "not_found"
            )
            await expect_error(
                client, "file:_:list", {"path": str(text_path)}, "not_directory"
            )
            current = await client.request("file:_:list", {"path": ""})
            assert current["path"] == str(home.resolve())
            assert [entry["name"] for entry in current["entries"]] == ["home.txt"]
            home_listing = await client.request("file:_:list", {"path": "~"})
            assert home_listing["path"] == str(home.resolve())
            assert [entry["name"] for entry in home_listing["entries"]] == ["home.txt"]
            listing = await client.request("file:_:list", {"path": str(listing_path)})
            assert listing["path"] == str(listing_path.resolve())
            assert [entry["name"] for entry in listing["entries"]] == [
                "A-dir",
                "z-dir",
                "a.txt",
                "B.json",
                "link.json",
            ]
            assert all(not entry["name"].startswith(".") for entry in listing["entries"])
            assert all(
                set(entry)
                == {
                    "name",
                    "path",
                    "type",
                    "size",
                    "mtime",
                    "mime",
                    "is_dir",
                    "is_symlink",
                    "link_target",
                }
                for entry in listing["entries"]
            )
            directories = listing["entries"][:2]
            assert all(
                entry["type"] == "directory"
                and entry["is_dir"] is True
                and entry["size"] is None
                and entry["mime"] is None
                for entry in directories
            )
            symlink = next(entry for entry in listing["entries"] if entry["name"] == "link.json")
            expected_symlink = {
                "name": "link.json",
                "path": str((listing_path / "B.json").resolve()),
                "type": "symlink",
                "size": (listing_path / "B.json").stat().st_size,
                "mtime": (listing_path / "B.json").stat().st_mtime,
                "mime": "application/json",
                "is_dir": False,
                "is_symlink": True,
                "link_target": str((listing_path / "B.json").resolve()),
            }
            assert symlink == expected_symlink, (symlink, expected_symlink)
            nested = await client.request(
                "file:_:list", {"path": str(listing_path / "A-dir")}
            )
            assert [entry["name"] for entry in nested["entries"]] == ["level-two"]
            deep = await client.request(
                "file:_:list", {"path": str(listing_path / "A-dir" / "level-two")}
            )
            assert [entry["name"] for entry in deep["entries"]] == ["deep.txt"]
            print("file list sorting/hidden/symlink/nesting/errors: PASS")
        finally:
            await client.close()
            if process.poll() is None:
                process.terminate()
                try:
                    await asyncio.to_thread(process.wait, 3)
                except subprocess.TimeoutExpired:
                    process.kill()
                    await asyncio.to_thread(process.wait)
            if process.returncode not in (0, -15):
                tail = log_path.read_text(errors="replace")[-4000:]
                raise AssertionError(f"file-service exit {process.returncode}:\n{tail}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--kernel-ws", default=os.environ.get("VIEWER_KERNEL_WS", "ws://127.0.0.1:29430/ws")
    )
    parser.add_argument(
        "--fileservice-bin", default=os.environ.get("VIEWER_FILESERVICE_BIN", "/tmp/viewer-fileservice")
    )
    asyncio.run(run(parser.parse_args()))


if __name__ == "__main__":
    main()
