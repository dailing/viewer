import { defineAsyncComponent } from "vue";

import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "plugin-manager",
  name: "插件管理",
  components: {
    "plugin-manager": defineAsyncComponent(() => import("./ManagerPane.vue")),
  },
  // Singleton manager: available from "+", without a permanent Dock entry.
  createDockProvider: () => ({
    type: "plugin-manager",
    icon: "bi-puzzle",
    title: "插件管理",
    singleton: true,
    instances: [],
  }),
});
