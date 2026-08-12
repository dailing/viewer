import { defineAsyncComponent } from "vue";

import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "terminal",
  components: {
    terminals: defineAsyncComponent(() => import("./TerminalsPanel.vue")),
    terminal: defineAsyncComponent(() => import("./TerminalPane.vue")),
  },
  sidebarTools: [{ id: "terminals", icon: "bi-terminal", title: "终端", paneType: "terminals" }],
});
