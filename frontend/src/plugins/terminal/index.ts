import { defineAsyncComponent, reactive } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { DockInstance, DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";

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

/**
 * Dock provider: live terminal instances from `terminal:*:status` mailboxes
 * (subscribe replays retained values, so the dock survives reconnects) plus a
 * one-shot `terminal:_:list` for immediacy. Only running terminals dock —
 * exited/killed ones drop out (their open panes keep showing the banner).
 */
function createDockProvider(ctx: PluginCtx): DockProvider {
  const byId = new Map<string, DockInstance>();
  const instances = reactive<DockInstance[]>([]);

  function sync(): void {
    instances.splice(
      0,
      instances.length,
      ...[...byId.values()].sort((a, b) => Number(a.id) - Number(b.id)),
    );
  }

  function upsert(status: TerminalStatus): void {
    if (status.state === "running") {
      byId.set(status.id, {
        id: status.id,
        label: `#${status.id} ${status.shell} · ${status.cwd} · ${status.cols}×${status.rows}`,
        state: status.state,
      });
    } else {
      byId.delete(status.id);
    }
    sync();
  }

  ctx.bus.subscribe("terminal:*:status", (frame) => {
    const value = frame.value as TerminalStatus;
    if (value && typeof value.id === "string") upsert(value);
  });
  ctx.bus
    .request("terminal:_:list")
    .then((list) => {
      for (const status of list as TerminalStatus[]) upsert(status);
    })
    .catch(() => undefined); // bus not connected yet — retained status mailboxes cover us

  return {
    type: "terminal",
    icon: "bi-terminal",
    title: "终端",
    instances,
    create: async () => {
      await ctx.bus.request("terminal:_:create", {});
    },
  };
}

export default definePlugin({
  id: "terminal",
  components: {
    terminal: defineAsyncComponent(() => import("./TerminalPane.vue")),
  },
  createDockProvider,
});
