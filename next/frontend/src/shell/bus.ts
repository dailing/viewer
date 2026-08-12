/**
 * F0: the shell's single bus connection (framework section 10.3).
 *
 * The browser never talks to the kernel directly — it connects to the
 * http-gateway's `/ws` on the same origin, which opens a dedicated kernel
 * connection per browser and pipes frames verbatim (protocol spec section 11).
 */

import { reactive } from "vue";

import { BusClient, type BusErrorEntry, type PluginManifest } from "@viewer/bus-sdk";

export const SHELL_MANIFEST: PluginManifest = {
  id: "shell",
  version: "0.1.0",
  slots: {},
  emits: {},
};

function busUrl(): string {
  const scheme = location.protocol === "https:" ? "wss" : "ws";
  return `${scheme}://${location.host}/ws`;
}

export const busState = reactive({
  connected: false,
  conn: null as string | null,
  /** Recent kernel-side protocol errors for this connection (surfaced, capped). */
  errors: [] as BusErrorEntry[],
});

export const bus = new BusClient(busUrl(), SHELL_MANIFEST, {
  backoffBase: 500,
  backoffCap: 10_000,
});

bus.onStateChange((connected) => {
  busState.connected = connected;
  busState.conn = bus.conn;
});

bus.onError((entry) => {
  busState.errors.push(entry);
  if (busState.errors.length > 100) busState.errors.shift();
});

export async function connectBus(): Promise<void> {
  await bus.connect();
}
