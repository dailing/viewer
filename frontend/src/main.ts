import { createPinia } from "pinia";
import { createApp } from "vue";

import "bootstrap/dist/css/bootstrap.min.css";
import "bootstrap-icons/font/bootstrap-icons.css";
import "katex/dist/katex.min.css";
import "./styles.css";

import App from "./App.vue";
import { connectBus } from "./shell/bus";
import { loadExternalPlugins, loadPlugins } from "./shell/loader";
import { useMarkdownStyleStore } from "./stores/markdownStyle";
import { useThemeStore } from "./stores/theme";

const app = createApp(App);
app.use(createPinia());
loadPlugins();
loadExternalPlugins();
app.mount("#app");
useThemeStore().init();
useMarkdownStyleStore().init();

// Bus connects after mount: panes render immediately and bind "connecting"
// state themselves (framework section 8.6 rule 5).
void connectBus();
