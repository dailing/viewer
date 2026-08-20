import path from "node:path";
import { fileURLToPath } from "node:url";

import vue from "@vitejs/plugin-vue";
import { defineConfig } from "vite";

const dirname = path.dirname(fileURLToPath(import.meta.url));
const gatewayTarget = process.env.VITE_VIEWER_GATEWAY_TARGET || "ws://127.0.0.1:18730";

export default defineConfig({
  plugins: [vue()],
  // Frontend build stamp, shown in the Dock foot so a glance tells which
  // build the browser is running. Recomputed on every `vite build`.
  define: {
    __BUILD_TIME__: JSON.stringify(new Date().toISOString()),
  },
  resolve: {
    alias: {
      "@viewer/bus-sdk": path.resolve(dirname, "../sdk/ts/src/index.ts"),
    },
  },
  build: {
    sourcemap: process.env.VIEWER_DEBUG === "1",
    rollupOptions: {
      input: {
        main: path.resolve(dirname, "index.html"),
        // Stage-B externals (framework 8.4): stable URLs the import map in
        // index.html points at; they share chunks with the app bundle so
        // external plugins get the shell's vue/pinia singletons.
        "externals-vue": path.resolve(dirname, "src/externals/vue.ts"),
        "externals-pinia": path.resolve(dirname, "src/externals/pinia.ts"),
      },
      output: {
        entryFileNames: (chunk) =>
          chunk.name.startsWith("externals-") ? "assets/[name].js" : "assets/[name]-[hash].js",
      },
      // The externals-* entries exist purely for their export surface (the
      // import map); without this, Rollup tree-shakes the `export *` down to
      // the handful of helpers the app bundle happens to use and external
      // plugin bundles fail with "does not provide an export named …".
      preserveEntrySignatures: "exports-only",
    },
  },
  server: {
    host: "0.0.0.0",
    proxy: {
      "/ws": { target: gatewayTarget, ws: true },
    },
  },
});
