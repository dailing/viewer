import { defineAsyncComponent } from "vue";

import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "bus-inspector",
  components: {
    "bus-inspector": defineAsyncComponent(() => import("./InspectorPane.vue")),
  },
  // Singleton: no backend-tracked instances — the Dock shows its entry while
  // a bus-inspector pane is open; "+" focuses/opens that pane.
  createDockProvider: () => ({
    type: "bus-inspector",
    icon: "bi-activity",
    title: "Bus Inspector",
    singleton: true,
    instances: [],
  }),
});
