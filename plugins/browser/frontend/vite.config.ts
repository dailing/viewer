import path from "node:path";
import { fileURLToPath } from "node:url";
import vue from "@vitejs/plugin-vue";
import { defineConfig } from "vite";

const dirname = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { "@viewer/bus-sdk": path.resolve(dirname, "../../../sdk/ts/src/index.ts") },
  },
  build: {
    lib: {
      entry: path.resolve(dirname, "src/index.ts"),
      formats: ["es"],
      fileName: () => "frontend.js",
    },
    cssCodeSplit: false,
    rollupOptions: {
      external: ["vue", "pinia"],
      output: { assetFileNames: "frontend.[ext]" },
    },
  },
});
