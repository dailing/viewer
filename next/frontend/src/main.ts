import { createPinia } from "pinia";
import { createApp } from "vue";

import "bootstrap/dist/css/bootstrap.min.css";
import "bootstrap-icons/font/bootstrap-icons.css";

import App from "./App.vue";
import { connectBus } from "./shell/bus";
import { loadPlugins } from "./shell/loader";

const app = createApp(App);
app.use(createPinia());
loadPlugins();
app.mount("#app");

// Bus connects after mount: panes render immediately and bind "connecting"
// state themselves (framework section 8.6 rule 5).
void connectBus();
