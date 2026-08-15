/**
 * BusClient integration tests against a REAL kernel process — the kernel is
 * the spec, no wire mocks (same convention as the Python test suite).
 *
 * Each suite spawns the Go `viewer-kernel` binary on a free port.
 */

import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { existsSync, mkdirSync } from "node:fs";
import { homedir } from "node:os";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import {
  BusClient,
  BusConnectionError,
  RpcError,
  RpcTimeoutError,
  type BusErrorEntry,
  type BusFrame,
} from "../src/index.js";

const REPO_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
const KERNEL_BIN = process.env.VIEWER_KERNEL_BIN
  ? path.resolve(process.env.VIEWER_KERNEL_BIN)
  : path.join(REPO_DIR, "bin", "viewer-kernel");

const TEST_MANIFEST = { id: "ts-sdk-test", version: "0.1.0", slots: {}, emits: {} };

async function freePort(): Promise<number> {
  return await new Promise((resolve, reject) => {
    const server = net.createServer();
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      const port = typeof address === "object" && address !== null ? address.port : 0;
      server.close(() => resolve(port));
    });
    server.on("error", reject);
  });
}

function ensureKernelBinary(): void {
  if (existsSync(KERNEL_BIN)) return;
  if (process.env.VIEWER_KERNEL_BIN) {
    throw new Error(`VIEWER_KERNEL_BIN does not exist: ${KERNEL_BIN}`);
  }

  mkdirSync(path.dirname(KERNEL_BIN), { recursive: true });
  const goBinDir = path.join(homedir(), ".local", "go", "bin");
  const result = spawnSync(
    "go",
    ["build", "-o", "bin/viewer-kernel", "./cmd/viewer-kernel"],
    {
      cwd: REPO_DIR,
      encoding: "utf8",
      env: { ...process.env, PATH: `${goBinDir}:${process.env.PATH ?? ""}` },
    },
  );
  if (result.status !== 0 || !existsSync(KERNEL_BIN)) {
    const detail = result.error?.message ?? result.stderr.trim() ?? `exit status ${result.status}`;
    throw new Error(`Failed to build Go viewer-kernel at ${KERNEL_BIN}: ${detail}`);
  }
}

function startKernel(port: number): ChildProcess {
  return spawn(KERNEL_BIN, ["--host", "127.0.0.1", "--port", String(port)], {
    cwd: REPO_DIR,
    stdio: ["ignore", "ignore", "pipe"],
  });
}

function stopKernel(child: ChildProcess): void {
  if (child.exitCode === null && child.signalCode === null) {
    child.kill("SIGTERM");
    const killTimer = setTimeout(() => {
      if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
    }, 1_000);
    killTimer.unref();
  }
}

function makeClient(port: number, options: Record<string, unknown> = {}): BusClient {
  return new BusClient(`ws://127.0.0.1:${port}/ws`, TEST_MANIFEST, {
    backoffBase: 100,
    backoffCap: 1000,
    ...options,
  });
}

/** Resolve once `predicate` holds, polling; reject after `timeoutMs`. */
async function waitFor(predicate: () => boolean, timeoutMs = 10_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error("waitFor timed out");
}

beforeAll(() => {
  ensureKernelBinary();
});

describe("BusClient against the real kernel", () => {
  let port = 0;
  let kernel: ChildProcess;

  beforeAll(async () => {
    port = await freePort();
    kernel = startKernel(port);
  });

  afterAll(async () => {
    stopKernel(kernel);
  });

  it("hello + registration barrier via plugins:_:list", async () => {
    const client = makeClient(port);
    await client.connect();
    expect(client.connected).toBe(true);
    expect(client.conn).toMatch(/^[0-9a-f-]{36}$/);
    await client.waitRegistered();
    await client.close();
  });

  it("publish/subscribe roundtrip with kernel-stamped origin and ts", async () => {
    const subscriber = makeClient(port);
    const publisher = makeClient(port, { instanceId: "pub1" });
    await subscriber.connect();
    await publisher.connect();

    const received: BusFrame[] = [];
    await subscriber.subscribe("chat:1:status", (frame) => received.push(frame));
    await publisher.publish("chat:1:status", { state: "open" });

    await waitFor(() => received.length === 1);
    const frame = received[0]!;
    expect(frame.value).toEqual({ state: "open" });
    expect(frame.origin).toEqual({ plugin: "ts-sdk-test", instance: "pub1" });
    expect(typeof frame.ts).toBe("number");
    await subscriber.close();
    await publisher.close();
  });

  it("mailbox retained replay: set before subscribe delivers the current value", async () => {
    const setter = makeClient(port);
    const reader = makeClient(port);
    await setter.connect();
    await reader.connect();

    await setter.set("chat:2:status", { state: "pinned" });
    const received: BusFrame[] = [];
    await reader.subscribe("chat:2:status", (frame) => received.push(frame));

    await waitFor(() => received.length === 1);
    expect(received[0]!.value).toEqual({ state: "pinned" });
    await setter.close();
    await reader.close();
  });

  it("RPC roundtrip resolves with result; ok:false rejects with RpcError", async () => {
    const responder = makeClient(port);
    const caller = makeClient(port);
    await responder.connect();
    await caller.connect();

    await responder.subscribe("math:_:double", (frame) => {
      const value = frame.value ?? {};
      if (typeof value._reply_to !== "string") return;
      const ok = typeof value.n === "number";
      void responder.publish(value._reply_to, {
        _corr: value._corr,
        ok,
        ...(ok ? { result: value.n * 2 } : { error: { code: "bad_request", message: "n must be a number" } }),
      });
    });

    await expect(caller.request("math:_:double", { n: 21 })).resolves.toBe(42);
    const failure = await caller.request("math:_:double", { n: "x" }).catch((error: Error) => error);
    expect(failure).toBeInstanceOf(RpcError);
    expect((failure as RpcError).code).toBe("bad_request");
    await responder.close();
    await caller.close();
  });

  it("RPC timeout rejects with RpcTimeoutError", async () => {
    const caller = makeClient(port);
    await caller.connect();
    const failure = await caller
      .request("nobody:_:listening", null, { timeout: 300 })
      .catch((error: Error) => error);
    expect(failure).toBeInstanceOf(RpcTimeoutError);
    await caller.close();
  });

  it("surfaces kernel protocol errors via the per-connection error mailbox", async () => {
    const client = makeClient(port);
    const errors: BusErrorEntry[] = [];
    client.onError((entry) => errors.push(entry));
    await client.connect();

    // Bypass SDK framing to send a protocol-invalid frame (unknown type).
    (client as unknown as { _send: (frame: Record<string, unknown>) => void })._send({
      type: "bogus",
      channel: "chat:1:status",
      value: {},
    });

    await waitFor(() => errors.length === 1);
    expect(errors[0]!.code).toBe("unknown_type");
    expect(client.connected).toBe(true); // connection stays up (spec section 10)
    await client.close();
  });

  it("pending RPCs reject with BusConnectionError when the bus drops", async () => {
    const caller = makeClient(port);
    await caller.connect();
    const pending = caller.request("nobody:_:listening");
    const assertion = expect(pending).rejects.toBeInstanceOf(BusConnectionError);
    await caller.close();
    await assertion;
  });
});

describe("BusClient survival reconnect (kernel restart)", () => {
  it("reconnects with a new conn and replays subscriptions", async () => {
    const port = await freePort();
    let kernel = startKernel(port);

    const subscriber = makeClient(port);
    await subscriber.connect();
    const firstConn = subscriber.conn;
    const received: BusFrame[] = [];
    await subscriber.subscribe("chat:3:status", (frame) => received.push(frame));

    // Kill the kernel; the subscriber must notice and start backing off.
    stopKernel(kernel);
    await waitFor(() => !subscriber.connected);

    // Restart on the same port; the subscriber must re-hello and replay.
    kernel = startKernel(port);
    await waitFor(() => subscriber.connected && subscriber.conn !== firstConn);

    const publisher = makeClient(port);
    await publisher.connect();
    await publisher.publish("chat:3:status", { state: "after-restart" });
    await waitFor(() => received.length === 1);
    expect(received[0]!.value).toEqual({ state: "after-restart" });

    await subscriber.close();
    await publisher.close();
    stopKernel(kernel);
  }, 20_000);
});
