import { defineAsyncComponent } from "vue";

import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "llm",
  name: "语言模型",
  settings: defineAsyncComponent(() => import("./LLMPane.vue")),
});
