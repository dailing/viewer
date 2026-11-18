import { defineAsyncComponent } from "vue";
import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "metrics",
  name: "Metrics",
  components: {
    metrics: defineAsyncComponent(() => import("./MetricsPane.vue")),
  },
  createDockProvider: () => ({
    type: "metrics",
    icon: "bi-bar-chart",
    title: "Metrics",
    instances: [],
  }),
});
