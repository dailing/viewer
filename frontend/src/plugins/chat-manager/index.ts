import { defineAsyncComponent } from "vue";

import type { DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "chat-manager",
  components: {
    "chat-manager": defineAsyncComponent(() => import("./ManagerPanel.vue")),
  },
  createDockProvider: (): DockProvider => ({
    type: "chat-manager",
    icon: "bi-chat-square-dots",
    title: "聊天管理",
    singleton: true,
    instances: [],
  }),
});
