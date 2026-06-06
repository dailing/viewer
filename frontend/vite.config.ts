import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

const apiTarget = process.env.VITE_VIEWER_API_TARGET || "http://127.0.0.1:18989";

export default defineConfig({
  plugins: [vue()],
  build: {
    sourcemap: process.env.VIEWER_DEBUG === "1",
  },
  server: {
    host: "0.0.0.0",
    proxy: {
      "/api": apiTarget,
    },
  },
});
