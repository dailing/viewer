import { defineAsyncComponent } from "vue";

import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "bus-inspector",
  name: "总线检查器",
  components: {
    "bus-inspector": defineAsyncComponent(() => import("./InspectorPane.vue")),
  },
  // Singleton tools are available from "+", without a permanent Dock entry.
  createDockProvider: () => ({
    type: "bus-inspector",
    icon: "bi-activity",
    title: "Bus Inspector",
    instances: [],
  }),
});
