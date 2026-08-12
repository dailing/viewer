import { defineAsyncComponent } from "vue";

import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "bus-inspector",
  components: {
    "bus-inspector": defineAsyncComponent(() => import("./InspectorPane.vue")),
  },
  sidebarTools: [
    { id: "bus-inspector", icon: "bi-activity", title: "Bus Inspector", paneType: "bus-inspector" },
  ],
});
