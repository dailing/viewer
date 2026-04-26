import "bootstrap/dist/css/bootstrap.min.css";
import "bootstrap-icons/font/bootstrap-icons.css";
import "katex/dist/katex.min.css";
import "highlight.js/styles/github.css";
import "./styles.css";

import { createApp } from "vue";
import { createPinia } from "pinia";
import App from "./App.vue";
import { installClientLogging } from "./utils/clientLog";

installClientLogging();
createApp(App).use(createPinia()).mount("#app");
