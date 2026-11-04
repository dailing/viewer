import { defineAsyncComponent } from "vue";
import { definePlugin } from "../../shell/definePlugin";

export default definePlugin({
  id: "settings",
  name: "界面设置",
  components: {
    settings: defineAsyncComponent(() => import("./SettingsPane.vue")),
  },
});
