import { defineAsyncComponent } from "vue";

import type { DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "llm",
  components: {
    llm: defineAsyncComponent(() => import("./LLMPane.vue")),
  },
  createDockProvider: (): DockProvider => ({
    type: "llm",
    icon: "bi-cpu",
    title: "语言模型",
    singleton: true,
    instances: [],
  }),
});
