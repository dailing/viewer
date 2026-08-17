import { defineAsyncComponent } from "vue";
import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "settings",
  components: {
    settings: defineAsyncComponent(() => import("./SettingsPane.vue")),
  },
});
