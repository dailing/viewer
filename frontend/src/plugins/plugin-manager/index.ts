import { defineAsyncComponent } from "vue";

import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "plugin-manager",
  components: {
    "plugin-manager": defineAsyncComponent(() => import("./ManagerPane.vue")),
  },
  // Singleton manager pane (framework 8.7): pinned Dock entry, "+" focuses it.
  createDockProvider: () => ({
    type: "plugin-manager",
    icon: "bi-puzzle",
    title: "插件管理",
    singleton: true,
    instances: [],
  }),
});
