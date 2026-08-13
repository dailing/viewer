/**
 * Low-level bus client: hello, reconnect with subscription replay, RPC inbox.
 *
 * TS port of `next/sdk/client.py` (Python SDK). Both implement the SDK duties
 * from the protocol spec:
 * - hello handshake with a fresh client-generated `conn` (uuid4) per attempt;
 * - auto-subscribe to the per-connection error mailbox and surface entries;
 * - exponential-backoff reconnect after close 4009 / abnormal loss, with
 *   subscription replay (spec section 7);
 * - RPC inbox convention: request/timeout/cancel (spec section 8);
 * - registration barrier via the `plugins:_:list` mailbox (spec section 5.2).
 *
 * Runs in the browser (against the http-gateway) and in Node >= 22 (built-in
 * WebSocket), so tests exercise the real kernel without a ws dependency.
 */

import { channelMatches } from "./matching.js";

export const PROTOCOL_VERSION = 1;

/**
 * UUIDv4. `crypto.randomUUID()` requires a SECURE context (https/localhost);
 * on plain-http LAN origins it is undefined, so fall back to
 * `crypto.getRandomValues`, which is available everywhere. The kernel
 * validates `conn` as UUIDv4 either way.
 */
export function uuidv4(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40; // version 4
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80; // variant 1
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0"));
  return (
    hex.slice(0, 4).join("") +
    "-" +
    hex.slice(4, 6).join("") +
    "-" +
    hex.slice(6, 8).join("") +
    "-" +
    hex.slice(8, 10).join("") +
    "-" +
    hex.slice(10, 16).join("")
  );
}

export interface Origin {
  plugin: string;
  instance: string;
}

/** A frame as delivered by the kernel to subscribers. */
export interface BusFrame {
  type: string;
  channel: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  value: any;
  ts?: number;
  origin?: Origin;
  trace_id?: string;
  depth?: number;
}

/** Plugin manifest; the kernel requires at least id/version/slots/emits. */
export interface PluginManifest {
  id: string;
  version: string;
  slots: Record<string, unknown>;
  emits: Record<string, unknown>;
  [extra: string]: unknown;
}

/** Entry delivered on the per-connection `_conn:{conn}:error` mailbox. */
export interface BusErrorEntry {
  code: string;
  message: string;
  ts?: number;
  detail?: Record<string, unknown>;
}

export type FrameHandler = (frame: BusFrame) => void;
export type ErrorHandler = (entry: BusErrorEntry) => void;
export type StateHandler = (connected: boolean) => void;

/** The bus connection dropped (or was never up) while an operation needed it. */
export class BusConnectionError extends Error {
  constructor(message = "not connected to the bus") {
    super(message);
    this.name = "BusConnectionError";
  }
}

/** The callee answered with `ok: false` (protocol spec section 8). */
export class RpcError extends Error {
  readonly code: string;
  constructor(code: string, message: string) {
    super(`${code}: ${message}`);
    this.name = "RpcError";
    this.code = code;
  }
}

/** No RPC response arrived within the timeout. */
export class RpcTimeoutError extends Error {
  readonly channel: string;
  readonly corr: string;
  constructor(channel: string, corr: string, timeoutMs: number) {
    super(`rpc timeout after ${timeoutMs}ms on ${channel} (corr ${corr})`);
    this.name = "RpcTimeoutError";
    this.channel = channel;
    this.corr = corr;
  }
}

export interface BusClientOptions {
  managed?: boolean;
  instanceId?: string;
  /** RPC timeout in ms (default 30_000, spec section 8). */
  requestTimeout?: number;
  /** Survival reconnect (default true). */
  reconnect?: boolean;
  /** Reconnect backoff base in ms (default 500). */
  backoffBase?: number;
  /** Reconnect backoff cap in ms (default 30_000). */
  backoffCap?: number;
}

interface PendingRpc {
  resolve: (result: unknown) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

export class BusClient {
  readonly url: string;
  readonly manifest: PluginManifest;
  readonly managed: boolean;
  readonly instanceId?: string;
  readonly requestTimeout: number;
  readonly reconnect: boolean;
  readonly backoffBase: number;
  readonly backoffCap: number;

  /** Current connection id; changes on every (re)hello. Null before first hello. */
  conn: string | null = null;

  private _ws: WebSocket | null = null;
  private _closing = false;
  private _attempt = 0;
  private _connected = false;
  private _runPromise: Promise<void> | null = null;
  private _firstConnected!: Promise<void>;
  private _markFirstConnected!: () => void;
  private readonly _handlers = new Map<string, Set<FrameHandler>>();
  private readonly _errorHandlers = new Set<ErrorHandler>();
  private readonly _stateHandlers = new Set<StateHandler>();
  private readonly _pendingRpc = new Map<string, PendingRpc>();

  constructor(url: string, manifest: PluginManifest, options: BusClientOptions = {}) {
    this.url = url;
    this.manifest = manifest;
    this.managed = options.managed ?? false;
    this.instanceId = options.instanceId;
    this.requestTimeout = options.requestTimeout ?? 30_000;
    this.reconnect = options.reconnect ?? true;
    this.backoffBase = options.backoffBase ?? 500;
    this.backoffCap = options.backoffCap ?? 30_000;
    this._resetFirstConnected();
  }

  get connected(): boolean {
    return this._connected;
  }

  get errorChannel(): string | null {
    return this.conn === null ? null : `_conn:${this.conn}:error`;
  }

  onError(callback: ErrorHandler): void {
    this._errorHandlers.add(callback);
  }

  /** Observe connect/disconnect transitions (e.g. drive a "connecting" UI). */
  onStateChange(callback: StateHandler): void {
    this._stateHandlers.add(callback);
  }

  /** Start the connection loop; resolves after the first successful hello. */
  async connect(): Promise<void> {
    if (this._runPromise === null) {
      this._runPromise = this._run();
    }
    await this._firstConnected;
  }

  async close(): Promise<void> {
    this._closing = true;
    this._ws?.close();
    if (this._runPromise !== null) {
      await this._runPromise.catch(() => undefined);
    }
  }

  // ------------------------------------------------------------------ frames

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  async publish(channel: string, value: any, options: { traceId?: string; depth?: number } = {}): Promise<void> {
    const frame: Record<string, unknown> = { type: "publish", channel, value };
    if (options.traceId !== undefined) frame.trace_id = options.traceId;
    if (options.depth) frame.depth = options.depth;
    this._send(frame);
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  async set(channel: string, value: any, options: { traceId?: string; depth?: number } = {}): Promise<void> {
    const frame: Record<string, unknown> = { type: "set", channel, value };
    if (options.traceId !== undefined) frame.trace_id = options.traceId;
    if (options.depth) frame.depth = options.depth;
    this._send(frame);
  }

  /** Register a pattern (replayed on reconnect) and optionally a handler. */
  async subscribe(pattern: string, handler?: FrameHandler): Promise<void> {
    let handlers = this._handlers.get(pattern);
    if (handlers === undefined) {
      handlers = new Set();
      this._handlers.set(pattern, handlers);
    }
    if (handler !== undefined) handlers.add(handler);
    if (this._connected) {
      this._send({ type: "subscribe", pattern });
    }
  }

  async unsubscribe(pattern: string, handler?: FrameHandler): Promise<void> {
    const handlers = this._handlers.get(pattern);
    if (handlers !== undefined) {
      if (handler === undefined) {
        this._handlers.delete(pattern);
      } else {
        if (!handlers.delete(handler)) return;
        if (handlers.size > 0) return;
        this._handlers.delete(pattern);
      }
    }
    if (this._connected) {
      this._send({ type: "unsubscribe", pattern });
    }
  }

  // --------------------------------------------------------------------- RPC

  /**
   * Inbox-convention RPC (spec section 8). Resolves with the `result` payload.
   * Rejects with `RpcError` on an `ok: false` response, `RpcTimeoutError`
   * after `timeout` (default 30s), `BusConnectionError` when the bus drops.
   */
  async request(
    channel: string,
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    value: any = null,
    options: { timeout?: number; traceId?: string; depth?: number } = {},
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ): Promise<any> {
    if (this.conn === null || !this._connected) {
      throw new BusConnectionError();
    }
    const corr = uuidv4().replace(/-/g, "");
    const inbox = `_inbox:${this.conn}:${corr}`;
    const timeout = options.timeout ?? this.requestTimeout;
    const payload: Record<string, unknown> =
      value !== null && typeof value === "object" && !Array.isArray(value)
        ? { ...value }
        : value === null
          ? {}
          : { value };
    payload._reply_to = inbox;
    payload._corr = corr;

    return await new Promise((resolve, reject) => {
      const pending: PendingRpc = {
        resolve: (result) => {
          clearTimeout(pending.timer);
          resolve(result);
        },
        reject: (error) => {
          clearTimeout(pending.timer);
          reject(error);
        },
        timer: setTimeout(() => {
          this._pendingRpc.delete(corr);
          reject(new RpcTimeoutError(channel, corr, timeout));
        }, timeout),
      };
      this._pendingRpc.set(corr, pending);
      const settle = (error?: Error) => {
        this._pendingRpc.delete(corr);
        if (error !== undefined) pending.reject(error);
      };
      (async () => {
        try {
          this._send({ type: "subscribe", pattern: inbox });
          await this.publish(channel, payload, { traceId: options.traceId, depth: options.depth });
        } catch (error) {
          settle(error instanceof Error ? error : new BusConnectionError());
        }
      })();
    }).finally(() => {
      if (this._connected) {
        try {
          this._send({ type: "unsubscribe", pattern: inbox });
        } catch {
          // Bus dropped mid-request; the inbox dies with the connection anyway.
        }
      }
    });
  }

  /** Best-effort cancel: the callee decides whether to honour it. */
  async cancel(channel: string, corr: string, options: { depth?: number } = {}): Promise<void> {
    await this.publish(channel, { _corr: corr, _cancel: true }, { depth: options.depth });
  }

  // ------------------------------------------------------------- registration

  /** Barrier: observe our own entry in the `plugins:_:list` mailbox. */
  async waitRegistered(timeout = 5000): Promise<void> {
    const myConn = this.conn;
    if (myConn === null) throw new BusConnectionError();
    const pattern = "plugins:_:list";
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        void this.unsubscribe(pattern, watcher).catch(() => undefined);
        reject(new RpcTimeoutError(pattern, myConn, timeout));
      }, timeout);
      const watcher: FrameHandler = (frame) => {
        const entries = Array.isArray(frame.value) ? frame.value : [];
        for (const entry of entries) {
          if (entry !== null && typeof entry === "object" && entry.conn === myConn) {
            clearTimeout(timer);
            void this.unsubscribe(pattern, watcher).catch(() => undefined);
            resolve();
            return;
          }
        }
      };
      void this.subscribe(pattern, watcher).catch((error: Error) => {
        clearTimeout(timer);
        reject(error);
      });
    });
  }

  // ------------------------------------------------------------------ intern

  private _resetFirstConnected(): void {
    this._firstConnected = new Promise<void>((resolve) => {
      this._markFirstConnected = resolve;
    });
  }

  private _send(frame: Record<string, unknown>): void {
    const ws = this._ws;
    if (ws === null || !this._connected || ws.readyState !== WebSocket.OPEN) {
      throw new BusConnectionError();
    }
    ws.send(JSON.stringify(frame));
  }

  private _setConnected(connected: boolean): void {
    if (this._connected === connected) return;
    this._connected = connected;
    for (const handler of this._stateHandlers) handler(connected);
  }

  private _failPendingRpc(error: Error): void {
    for (const pending of this._pendingRpc.values()) {
      clearTimeout(pending.timer);
      pending.reject(error);
    }
    this._pendingRpc.clear();
  }

  private async _run(): Promise<void> {
    while (!this._closing) {
      try {
        await this._serveOnce();
      } catch {
        // Connection refused, abnormal close, etc. — handled by reconnect below.
      }
      this._ws = null;
      this._setConnected(false);
      this._failPendingRpc(new BusConnectionError("bus connection lost"));
      if (!this.reconnect || this._closing) return;
      this._attempt += 1;
      const delay = Math.min(this.backoffBase * 2 ** (this._attempt - 1), this.backoffCap);
      await sleep(delay);
    }
  }

  /** One connection lifetime: open, hello, replay, serve until close. */
  private async _serveOnce(): Promise<void> {
    this.conn = uuidv4();
    const hello: Record<string, unknown> = {
      type: "hello",
      protocol_version: PROTOCOL_VERSION,
      conn: this.conn,
      manifest: this.manifest,
      managed: this.managed,
    };
    if (this.instanceId !== undefined) hello.instance_id = this.instanceId;

    const ws = new WebSocket(this.url);
    this._ws = ws;
    await new Promise<void>((resolve, reject) => {
      ws.onopen = () => resolve();
      ws.onerror = () => reject(new BusConnectionError("websocket connect failed"));
      ws.onclose = () => reject(new BusConnectionError("websocket closed during hello"));
    });

    // From here the socket is open; serve messages until it closes.
    await new Promise<void>((resolve) => {
      ws.onclose = () => resolve();
      ws.onerror = () => undefined;
      ws.onmessage = (event: MessageEvent<string>) => {
        try {
          this._dispatch(JSON.parse(event.data) as BusFrame);
        } catch {
          // Malformed frame from the bus side; ignore (kernel never sends any).
        }
      };
      ws.send(JSON.stringify(hello));
      // SDK duty: auto-subscribe the per-connection error mailbox.
      if (this.errorChannel !== null) {
        ws.send(JSON.stringify({ type: "subscribe", pattern: this.errorChannel }));
      }
      // SDK duty: replay subscriptions after (re)hello.
      for (const pattern of this._handlers.keys()) {
        ws.send(JSON.stringify({ type: "subscribe", pattern }));
      }
      this._attempt = 0;
      this._setConnected(true);
      this._markFirstConnected();
    });
  }

  private _dispatch(frame: BusFrame): void {
    const channel = frame.channel ?? "";
    const value = frame.value;
    if (channel === this.errorChannel && value !== null && typeof value === "object") {
      for (const callback of this._errorHandlers) callback(value as BusErrorEntry);
    }
    if (channel.startsWith("_inbox:") && value !== null && typeof value === "object") {
      const corr = typeof value._corr === "string" ? value._corr : null;
      const pending = corr === null ? undefined : this._pendingRpc.get(corr);
      if (corr !== null && pending !== undefined) {
        this._pendingRpc.delete(corr);
        if (value.ok === false) {
          const error = value.error ?? {};
          pending.reject(new RpcError(String(error.code ?? "error"), String(error.message ?? "")));
        } else {
          pending.resolve(value.result);
        }
      }
    }
    for (const [pattern, handlers] of this._handlers) {
      if (channelMatches(pattern, channel)) {
        for (const handler of handlers) handler(frame);
      }
    }
  }
}
