/**
 * Executable contracts for the bus calls made by next/frontend's terminal and
 * Bus Inspector panes. The suite talks to the real plugins inside viewerd.
 */

import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { BusClient, type BusFrame } from "../src/index.js";

const NEXT_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const REPO_DIR = path.resolve(NEXT_DIR, "..");
const NEXT_GO_DIR = path.join(REPO_DIR, "next-go");
const VIEWERD_BIN = process.env.VIEWERD_BIN
  ? path.resolve(process.env.VIEWERD_BIN)
  : path.join(NEXT_GO_DIR, "bin", "viewerd");
const MANIFEST = { id: "frontend-contract", version: "0.1.0", slots: {}, emits: {} };
const FILES_MANIFEST = { ...MANIFEST, id: "frontend-files-contract" };

interface TerminalStatus {
  id: string;
  state: "running" | "exited" | "killed";
  exit_code: number | null;
  pid: number;
  cwd: string;
  shell: string;
  cols: number;
  rows: number;
  created_ts: number;
}

interface OutputEntry {
  seq: number;
  ts: number;
  data: string;
}

interface InspectorEntry {
  seq: number;
  ts: number;
  type: string;
  channel: string;
  origin: { plugin?: string; instance?: string };
  trace_id: string;
  depth: number;
  value: unknown;
}

interface InspectorStats {
  captured: number;
  emitted: number;
  dropped: number;
  rate_per_sec: number;
  paused: boolean;
  filter: Record<string, string>;
  ring_size: number;
  ring_used: number;
}

interface FileEntry {
  name: string;
  path: string;
  type: "file" | "directory" | "symlink" | "other";
  size: number | null;
  mtime: number | null;
  mime: string | null;
  is_dir: boolean;
  is_symlink: boolean;
  link_target: string | null;
}

interface DirectoryListing {
  path: string;
  entries: FileEntry[];
}

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

async function waitFor(predicate: () => boolean, timeoutMs = 10_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error("waitFor timed out");
}

function ensureViewerdBinary(): void {
  if (process.env.VIEWERD_BIN) {
    if (!existsSync(VIEWERD_BIN)) throw new Error(`VIEWERD_BIN does not exist: ${VIEWERD_BIN}`);
    return;
  }
  mkdirSync(path.dirname(VIEWERD_BIN), { recursive: true });
  const goBinDir = path.join(homedir(), ".local", "go", "bin");
  const result = spawnSync("go", ["build", "-o", "bin/viewerd", "./cmd/viewerd"], {
    cwd: NEXT_GO_DIR,
    encoding: "utf8",
    env: { ...process.env, PATH: `${goBinDir}:${process.env.PATH ?? ""}` },
  });
  if (result.status !== 0 || !existsSync(VIEWERD_BIN)) {
    const detail = result.error?.message ?? result.stderr.trim() ?? `exit status ${result.status}`;
    throw new Error(`Failed to build Go viewerd at ${VIEWERD_BIN}: ${detail}`);
  }
}

function startViewerd(gatewayPort: number, kernelPort: number, dataDir: string, plugins = "bus-inspector,terminal,file-service"): ChildProcess {
  return spawn(
    VIEWERD_BIN,
    [
      "--host",
      "127.0.0.1",
      "--port",
      String(gatewayPort),
      "--kernel-host",
      "127.0.0.1",
      "--kernel-port",
      String(kernelPort),
      `--plugins=${plugins}`,
      "--data-dir",
      dataDir,
    ],
    {
      cwd: NEXT_GO_DIR,
      detached: true,
      stdio: ["ignore", "ignore", "pipe"],
      env: {
        ...process.env,
        VIEWER_HERMES_COMMAND: path.join(NEXT_GO_DIR, "scripts", "mock_acp_agent.py"),
        VIEWER_HERMES_PROFILE: "frontend-contract",
      },
    },
  );
}

async function stopViewerd(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) return;
  await new Promise<void>((resolve) => {
    const timer = setTimeout(() => child.kill("SIGKILL"), 2_000);
    timer.unref();
    child.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
    child.kill("SIGTERM");
  });
}

describe.sequential("next/frontend pane contracts against viewerd", () => {
  let gatewayPort = 0;
  let kernelPort = 0;
  let dataDir = "";
  let filesFixture = "";
  let viewerd: ChildProcess;

  beforeAll(async () => {
    ensureViewerdBinary();
    gatewayPort = await freePort();
    kernelPort = await freePort();
    dataDir = mkdtempSync(path.join(tmpdir(), "viewer-frontend-contract-"));
    filesFixture = path.join(dataDir, "files-fixture");
    mkdirSync(path.join(filesFixture, "Alpha", "nested"), { recursive: true });
    mkdirSync(path.join(filesFixture, "zulu"));
    writeFileSync(path.join(filesFixture, "Alpha", "nested", "deep.txt"), "deep");
    writeFileSync(path.join(filesFixture, "Alpha", ".nested-hidden"), "secret");
    writeFileSync(path.join(filesFixture, "a.txt"), "alpha");
    writeFileSync(path.join(filesFixture, "B.json"), "{}");
    writeFileSync(path.join(filesFixture, ".hidden"), "secret");
    symlinkSync(path.join(filesFixture, "B.json"), path.join(filesFixture, "link.json"));
    viewerd = startViewerd(gatewayPort, kernelPort, dataDir);
    let stderr = "";
    viewerd.stderr?.on("data", (chunk: Buffer) => {
      if (stderr.length < 16_384) stderr += chunk.toString().slice(0, 16_384 - stderr.length);
    });
    viewerd.once("exit", (code) => {
      if (code !== null && code !== 0) process.stderr.write(`viewerd exited ${code}: ${stderr}\n`);
    });
    const probe = new BusClient(`ws://127.0.0.1:${kernelPort}/ws`, MANIFEST, {
      backoffBase: 25,
      backoffCap: 100,
    });
    let registry: Array<{ manifest: { id: string } }> = [];
    await probe.subscribe("plugins:_:list", (frame) => {
      registry = frame.value as Array<{ manifest: { id: string } }>;
    });
    await probe.connect();
    await probe.waitRegistered();
    await waitFor(() => ["terminal", "file-service", "bus-inspector"].every((id) => registry.some((entry) => entry.manifest.id === id)));
    await probe.close();
  }, 30_000);

  afterAll(async () => {
    await stopViewerd(viewerd);
    rmSync(dataDir, { recursive: true, force: true });
  });

  it("drives terminal create/list/write/output/resize/snapshot/replay/kill", async () => {
    const client = new BusClient(`ws://127.0.0.1:${kernelPort}/ws`, MANIFEST, {
      backoffBase: 25,
      backoffCap: 100,
    });
    const statuses: TerminalStatus[] = [];
    const output: OutputEntry[] = [];
    await client.subscribe("terminal:*:status", (frame) => statuses.push(frame.value as TerminalStatus));
    await client.subscribe("terminal:*:output", (frame) => output.push(frame.value as OutputEntry));
    await client.connect();

    const created = (await client.request("terminal:_:create", { cols: 90, rows: 24 })) as { id: string };
    expect(created.id).toMatch(/^\d+$/);
    await waitFor(() => statuses.some((item) => item.id === created.id && item.state === "running"));

    const listed = (await client.request("terminal:_:list")) as TerminalStatus[];
    expect(listed).toEqual(expect.arrayContaining([expect.objectContaining({ id: created.id, cols: 90, rows: 24 })]));

    const marker = `__M5_TERMINAL_${Date.now()}__`;
    await client.request(`terminal:${created.id}:write`, { data: `printf '${marker}\\n'\n` });
    await waitFor(() => output.map((entry) => entry.data).join("").includes(marker));

    await client.request(`terminal:${created.id}:resize`, { cols: 101, rows: 37 });
    await waitFor(() => statuses.some((item) => item.id === created.id && item.cols === 101 && item.rows === 37));

    const snapshot = (await client.request(`terminal:${created.id}:snapshot`, { limit: 500 })) as {
      entries: OutputEntry[];
    };
    expect(snapshot.entries.map((entry) => entry.data).join("")).toContain(marker);
    expect(snapshot.entries.map((entry) => entry.seq)).toEqual(
      [...snapshot.entries.map((entry) => entry.seq)].sort((a, b) => a - b),
    );

    statuses.length = 0;
    const firstConn = client.conn;
    (client as unknown as { _ws: WebSocket })._ws.close();
    await waitFor(() => client.connected && client.conn !== firstConn);
    await waitFor(() => statuses.some((item) => item.id === created.id && item.cols === 101));

    await client.request(`terminal:${created.id}:kill`);
    await waitFor(() => statuses.some((item) => item.id === created.id && item.state === "killed"));
    await client.close();
  }, 30_000);

  it("drives inspector compound filters, pause/resume, clear, streams, stats, and self filtering", async () => {
    const client = new BusClient(`ws://127.0.0.1:${kernelPort}/ws`, MANIFEST);
    const matches: InspectorEntry[] = [];
    const stats: InspectorStats[] = [];
    await client.subscribe("bus-inspector:_:matches", (frame) => matches.push(frame.value as InspectorEntry));
    await client.subscribe("bus-inspector:_:stats", (frame) => stats.push(frame.value as InspectorStats));
    await client.connect();
    await waitFor(() => stats.length > 0);

    const traceId = `frontend-contract-${Date.now()}`;
    const marker = `payload-${Date.now()}`;
    const filter = {
      channel: "m5-contract:*:event",
      type: "publish",
      origin: MANIFEST.id,
      trace_id: traceId,
      text: marker,
    };
    await expect(client.request("bus-inspector:_:set-filter", filter)).resolves.toEqual({ filter });

    const other = new BusClient(`ws://127.0.0.1:${kernelPort}/ws`, {
      ...MANIFEST,
      id: "frontend-contract-other",
    });
    await other.connect();
    await client.publish("other:good:event", { marker }, { traceId });
    await client.set("m5-contract:type:event", { marker }, { traceId });
    await other.publish("m5-contract:origin:event", { marker }, { traceId });
    await client.publish("m5-contract:wrong:event", { marker }, { traceId: "wrong-trace" });
    await client.publish("m5-contract:text:event", { marker: "wrong-payload" }, { traceId });
    await client.publish("m5-contract:good:event", { marker }, { traceId });
    await waitFor(() => matches.some((entry) => entry.channel === "m5-contract:good:event"));
    expect(matches.filter((entry) => entry.channel.startsWith("m5-contract:"))).toHaveLength(1);
    expect(matches.at(-1)).toEqual(
      expect.objectContaining({
        type: "publish",
        channel: "m5-contract:good:event",
        trace_id: traceId,
        origin: expect.objectContaining({ plugin: MANIFEST.id }),
      }),
    );
    await other.close();

    const beforePause = matches.length;
    await expect(client.request("bus-inspector:_:pause")).resolves.toEqual({ paused: true });
    await client.publish("m5-contract:paused:event", { marker }, { traceId });
    await new Promise((resolve) => setTimeout(resolve, 150));
    expect(matches).toHaveLength(beforePause);
    await waitFor(() => stats.some((item) => item.paused));

    await expect(client.request("bus-inspector:_:resume")).resolves.toEqual({ paused: false });
    await client.publish("m5-contract:resumed:event", { marker }, { traceId });
    await waitFor(() => matches.some((entry) => entry.channel === "m5-contract:resumed:event"));

    const snapshot = (await client.request("bus-inspector:_:snapshot", { limit: 500 })) as {
      entries: InspectorEntry[];
    };
    expect(snapshot.entries.some((entry) => entry.channel === "m5-contract:paused:event")).toBe(true);
    expect(snapshot.entries.every((entry) => entry.origin.plugin !== "bus-inspector")).toBe(true);
    expect(snapshot.entries.map((entry) => entry.seq)).toEqual(
      [...snapshot.entries.map((entry) => entry.seq)].sort((a, b) => b - a),
    );
    const oldest = snapshot.entries.at(-1)?.seq;
    if (oldest !== undefined) {
      const older = (await client.request("bus-inspector:_:snapshot", {
        limit: 10,
        before_seq: oldest,
      })) as { entries: InspectorEntry[] };
      expect(older.entries.every((entry) => entry.seq < oldest)).toBe(true);
    }

    await expect(client.request("bus-inspector:_:clear")).resolves.toEqual({ cleared: true });
    await waitFor(() => stats.some((item) => item.ring_used === 0));
    expect(stats.at(-1)).toEqual(
      expect.objectContaining({
        paused: false,
        filter,
        ring_size: expect.any(Number),
        rate_per_sec: expect.any(Number),
      }),
    );
    await client.close();
  }, 30_000);

  it("lists lazy nested file trees with production fields, sorting, hidden filtering, symlinks, and errors", async () => {
    const client = new BusClient(`ws://127.0.0.1:${kernelPort}/ws`, FILES_MANIFEST);
    await client.connect();

    const current = (await client.request("file:_:list", { path: "" })) as DirectoryListing;
    expect(current.path).toBe(NEXT_GO_DIR);

    const root = (await client.request("file:_:list", {
      path: filesFixture,
    })) as DirectoryListing;
    expect(root.path).toBe(filesFixture);
    expect(root.entries.map((entry) => entry.name)).toEqual([
      "Alpha",
      "zulu",
      "a.txt",
      "B.json",
      "link.json",
    ]);
    expect(root.entries.some((entry) => entry.name.startsWith("."))).toBe(false);
    expect(root.entries.every((entry) => Object.keys(entry).sort().join(",") === [
      "is_dir",
      "is_symlink",
      "link_target",
      "mime",
      "mtime",
      "name",
      "path",
      "size",
      "type",
    ].join(","))).toBe(true);
    expect(root.entries.slice(0, 2)).toEqual([
      expect.objectContaining({ name: "Alpha", type: "directory", is_dir: true, size: null, mime: null }),
      expect.objectContaining({ name: "zulu", type: "directory", is_dir: true, size: null, mime: null }),
    ]);

    const target = path.join(filesFixture, "B.json");
    expect(root.entries.find((entry) => entry.name === "link.json")).toEqual({
      name: "link.json",
      path: target,
      type: "symlink",
      size: 2,
      mtime: expect.any(Number),
      mime: "application/json",
      is_dir: false,
      is_symlink: true,
      link_target: target,
    });

    // These are the two lazy expansion calls made after the root is visible.
    const firstExpansion = (await client.request("file:_:list", {
      path: path.join(filesFixture, "Alpha"),
    })) as DirectoryListing;
    expect(firstExpansion.entries.map((entry) => entry.name)).toEqual(["nested"]);
    const secondExpansion = (await client.request("file:_:list", {
      path: path.join(filesFixture, "Alpha", "nested"),
    })) as DirectoryListing;
    expect(secondExpansion.entries.map((entry) => entry.name)).toEqual(["deep.txt"]);

    await expect(
      client.request("file:_:list", { path: path.join(filesFixture, "missing") }),
    ).rejects.toMatchObject({ name: "RpcError", code: "not_found" });
    await expect(
      client.request("file:_:list", { path: path.join(filesFixture, "a.txt") }),
    ).rejects.toMatchObject({ name: "RpcError", code: "not_directory" });
    await client.close();
  }, 30_000);

  it("drives chat roles/chats/active mailbox, dual-role relay, busy, and router errors", async () => {
    const chatGatewayPort = await freePort();
    const chatKernelPort = await freePort();
    const chatDataDir = mkdtempSync(path.join(tmpdir(), "viewer-chat-contract-"));
    const chatViewerd = startViewerd(chatGatewayPort, chatKernelPort, chatDataDir, "config-store,chat");
    const client = new BusClient(`ws://127.0.0.1:${chatKernelPort}/ws`, { ...MANIFEST, id: "frontend-chat-contract" });
    const messages: Array<Record<string, unknown>> = [];
    const completed: Array<Record<string, unknown>> = [];
    const active: unknown[] = [];
    await client.subscribe("chat:*:message", (frame) => messages.push(frame.value as Record<string, unknown>));
    await client.subscribe("chat:*:turn-completed", (frame) => completed.push(frame.value as Record<string, unknown>));
    await client.subscribe("chat:_:active", (frame) => active.push(frame.value));
    let registry: Array<{ manifest: { id: string } }> = [];
    await client.subscribe("plugins:_:list", (frame) => { registry = frame.value as Array<{ manifest: { id: string } }>; });
    await client.connect();
    await waitFor(() => ["chat", "config-store"].every((id) => registry.some((entry) => entry.manifest.id === id)));

    const first = (await client.request("chat:_:roles:create", { name: "One", description: "first", prompt: "ONE", provider: "hermes" })) as { id: string };
    const second = (await client.request("chat:_:roles:create", { name: "Two", description: "second", prompt: "TWO", provider: "hermes" })) as { id: string };
    expect(await client.request("chat:_:roles:list", {})).toEqual(expect.arrayContaining([expect.objectContaining({ id: first.id }), expect.objectContaining({ id: second.id })]));
    await expect(client.request("chat:_:roles:create", { name: "Later", provider: "codex" })).rejects.toMatchObject({ code: "unsupported_provider" });
    const chat = (await client.request("chat:_:chats:create", { name: "Contract", root: NEXT_GO_DIR, member_role_ids: [first.id, second.id] })) as { id: string };
    await client.request("chat:_:chats:patch", { id: chat.id, pinned: true, name: "Contract renamed" });
    await client.request("chat:_:chats:activate", { id: chat.id });
    await waitFor(() => active.at(-1) === chat.id);
    expect(await client.request("chat:_:chats:list", {})).toEqual(expect.objectContaining({ active_chat_id: chat.id, chats: [expect.objectContaining({ id: chat.id, pinned: true })] }));

    const dispatched = (await client.request("chat:_:dispatch", { chat_id: chat.id, message: "hello", role_ids: [first.id, second.id] })) as { role_ids: string[] };
    expect(dispatched.role_ids).toEqual([first.id, second.id]);
    await waitFor(() => completed.filter((item) => item.chat_id === chat.id).length === 2);
    const relay = completed.filter((item) => item.chat_id === chat.id);
    expect(relay.map((item) => item.role_id)).toEqual([first.id, second.id]);
    expect(relay.map((item) => item.sender)).toEqual([
      { from: "role", role_id: first.id, role_name: "One" },
      { from: "role", role_id: second.id, role_name: "Two" },
    ]);
    expect(messages.find((item) => item.chat_id === chat.id && item.role === "user")?.sender).toEqual({ from: "user" });

    const beforeLong = messages.length;
    const longRun = (await client.request("chat:_:dispatch", { chat_id: chat.id, message: "long turn", role_ids: [first.id] })) as { dispatch_id: string };
    expect(longRun.dispatch_id).toBeTruthy();
    await waitFor(() => messages.slice(beforeLong).some((item) => item.chat_id === chat.id && item.sender && (item.sender as { role_id?: string }).role_id === first.id && String(item.text).includes("mock:")));
    await expect(client.request("chat:_:dispatch", { chat_id: chat.id, message: "busy", role_ids: [first.id] })).rejects.toMatchObject({ code: "routing_target_busy" });
    await client.request("chat:_:stop", { chat_id: chat.id, role_id: first.id });
    await waitFor(() => completed.some((item) => item.chat_id === chat.id && item.role_id === first.id && item.stop_reason === "cancelled"));
    await expect(client.request("chat:_:dispatch", { chat_id: chat.id, message: "auto" })).rejects.toMatchObject({ code: "chat_error" });
    await client.request("chat:_:chats:delete", { id: chat.id });
    await client.request("chat:_:roles:delete", { id: first.id });
    await client.request("chat:_:roles:delete", { id: second.id });
    await client.close();
    await stopViewerd(chatViewerd);
    rmSync(chatDataDir, { recursive: true, force: true });
  }, 30_000);
});
