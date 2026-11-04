import { defineAsyncComponent } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";

/** Terminal is a "+" launcher only; running PTYs do not occupy the Dock. */
function createDockProvider(ctx: PluginCtx): DockProvider {
  return {
    type: "terminal",
    icon: "bi-terminal",
    title: "终端",
    instances: [],
    create: async () => {
      const result = await ctx.bus.request("terminal:_:create", {}) as { id: string };
      return result.id;
    },
  };
}

export default definePlugin({
  id: "terminal",
  name: "终端",
  components: {
    terminal: defineAsyncComponent(() => import("./TerminalPane.vue")),
  },
  createDockProvider,
});
