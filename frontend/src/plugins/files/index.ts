import { defineAsyncComponent } from "vue";

import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "files",
  components: {
    files: defineAsyncComponent(() => import("./FilesPane.vue")),
  },
  createDockProvider: () => ({
    type: "files",
    icon: "bi-files",
    title: "文件",
    singleton: true,
    instances: [],
  }),
});
