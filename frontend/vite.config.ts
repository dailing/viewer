import path from "node:path";
import { fileURLToPath } from "node:url";

import vue from "@vitejs/plugin-vue";
import { defineConfig } from "vite";

const dirname = path.dirname(fileURLToPath(import.meta.url));
const gatewayTarget = process.env.VITE_VIEWER_GATEWAY_TARGET || "ws://127.0.0.1:18730";

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      "@viewer/bus-sdk": path.resolve(dirname, "../sdk/ts/src/index.ts"),
    },
  },
  build: {
    sourcemap: process.env.VIEWER_DEBUG === "1",
  },
  server: {
    host: "0.0.0.0",
    proxy: {
      "/ws": { target: gatewayTarget, ws: true },
    },
  },
});
