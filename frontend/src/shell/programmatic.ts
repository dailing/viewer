import type { BusClient, BusFrame } from "@viewer/bus-sdk";
import type { InstanceOpenRequest } from "./definePlugin";
import { openShellInstance } from "./instances";
import { closeShellPane, focusShellPane, listShellPanes, moveShellPane } from "./panes";
import type { PluginShellApi } from "./ctx";

interface RpcPayload { _reply_to?: unknown; _corr?: unknown; [key: string]: unknown }
type Publisher = (channel: string, value: unknown) => Promise<void>;

function object(value: unknown): RpcPayload {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as RpcPayload : {};
}
function requiredString(payload: RpcPayload, key: string): string {
  const value = payload[key];
  if (typeof value !== "string" || value === "") throw new Error(`${key} is required`);
  return value;
}
async function reply(publish: Publisher, payload: RpcPayload, run: () => unknown | Promise<unknown>): Promise<void> {
  if (typeof payload._reply_to !== "string" || typeof payload._corr !== "string") return;
  try {
    const result = await run();
    await publish(payload._reply_to, { _corr: payload._corr, ok: true, result });
  } catch (cause) {
    await publish(payload._reply_to, { _corr: payload._corr, ok: false, error: { code: "shell_error", message: cause instanceof Error ? cause.message : String(cause) } });
  }
}

export function createProgrammaticShellHandler(api: PluginShellApi, publish: Publisher): (frame: BusFrame) => void {
  return (frame) => {
    const payload = object(frame.value);
    const channel = frame.channel ?? "";
    void reply(publish, payload, async () => {
      if (channel.endsWith(":open")) {
        const request: InstanceOpenRequest = {
          paneType: requiredString(payload, "pane_type"),
          instanceId: typeof payload.instance_id === "string" ? payload.instance_id : undefined,
          params: payload.params,
          newPane: payload.new_pane === true,
        };
        return await api.open(request);
      }
      if (channel.endsWith(":panes:list")) return { panes: api.panes.list() };
      if (channel.endsWith(":panes:close")) { api.panes.close(requiredString(payload, "pane_id")); return { ok: true }; }
      if (channel.endsWith(":panes:focus")) { api.panes.focus(requiredString(payload, "pane_id")); return { ok: true }; }
      if (channel.endsWith(":panes:move")) {
        const delta = payload.delta;
        if (delta !== -1 && delta !== 1) throw new Error("delta must be -1 or 1");
        api.panes.move(requiredString(payload, "pane_id"), delta);
        return { ok: true };
      }
      throw new Error("unknown shell RPC");
    });
  };
}

const defaultApi: PluginShellApi = {
  open: openShellInstance,
  panes: { list: listShellPanes, close: closeShellPane, focus: focusShellPane, move: moveShellPane },
};
let activeConn: string | null = null;

/** Replayed by the shell on every reconnect. Old connection channels are
 * explicitly removed so BusClient never replays stale browser targets. */
export function syncProgrammaticShell(client: BusClient, conn: string | null): void {
  if (activeConn === conn) return;
  if (activeConn !== null) {
    for (const suffix of ["open", "panes:list", "panes:focus", "panes:close", "panes:move"]) {
      void client.unsubscribe(`shell:${activeConn}:${suffix}`).catch(() => undefined);
    }
  }
  activeConn = conn;
  if (conn === null) return;
  const handler = createProgrammaticShellHandler(defaultApi, (channel, value) => client.publish(channel, value));
  for (const suffix of ["open", "panes:list", "panes:focus", "panes:close", "panes:move"]) {
    void client.subscribe(`shell:${conn}:${suffix}`, handler).catch(() => undefined);
  }
}
