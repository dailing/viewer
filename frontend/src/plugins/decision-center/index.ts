import { defineAsyncComponent, ref } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import type { DecisionList } from "../chat/types";
import { registryHasChat } from "./runtime";

function createDockProvider(ctx: PluginCtx): DockProvider {
  const pending = ref(0);
  const refresh = async (): Promise<void> => {
    const result = await ctx.bus.request("chat:_:decisions:list", { pending: true, limit: 1, include_pending_count: true }) as DecisionList;
    pending.value = result.pending_count ?? 0;
  };
  const refreshSoon = (): void => { void refresh().catch(() => undefined); };
  ctx.bus.subscribe("chat:_:decision", refreshSoon);
  ctx.bus.subscribe("plugins:_:list", (frame) => {
    if (registryHasChat(frame.value)) refreshSoon();
  });
  refreshSoon();
  return {
    type: "decision-center",
    icon: "bi-inbox",
    title: "Decisions",
    instances: [{
      id: "main",
      label: "Decisions",
      meta: () => pending.value > 99 ? "99+" : pending.value > 0 ? pending.value : "",
      open: () => ctx.shell.open({ paneType: "decision-center", instanceId: "main" }),
    }],
  };
}

export default definePlugin({
  id: "decision-center",
  name: "Decisions",
  components: {
    "decision-center": defineAsyncComponent(() => import("./DecisionCenterPane.vue")),
  },
  createDockProvider,
  createInstanceAdapters: () => [{
    type: "decision-center",
    cardinality: "singleton",
    open() { return "main"; },
  }],
});
