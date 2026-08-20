import { defineAsyncComponent, reactive, watch, watchEffect } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { DockInstance, DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import { useLayoutStore } from "../../stores/layout";
import {
  createInstance,
  instances as registry,
  pruneUnpinned,
} from "./instanceStore";

function openFilesIds(layout: ReturnType<typeof useLayoutStore>): Set<string> {
  const ids = new Set<string>();
  for (const pane of layout.panes) {
    if (pane.content?.paneType === "files") ids.add(pane.content.instanceId);
  }
  return ids;
}

function createDockProvider(_ctx: PluginCtx): DockProvider {
  const layout = useLayoutStore();
  const instances = reactive<DockInstance[]>([]);
  watchEffect(() => {
    instances.splice(
      0,
      instances.length,
      ...registry.map((entry) => ({
        id: entry.id,
        label: entry.label,
        icon: entry.state.file !== null ? "bi-file-earmark-text" : "bi-folder2",
      })),
    );
  });

  // Unpinned instances live only while a pane hosts them; closing the pane
  // (or reloading the page) deletes them. Pinned ones stay in the Dock.
  const prune = (): void => pruneUnpinned(openFilesIds(layout));
  watch(() => layout.panes.map((pane) => pane.content), prune, { deep: true });
  prune();

  return {
    type: "files",
    icon: "bi-files",
    title: "文件",
    // The root entry is a launcher: clicking it opens a fresh instance pane;
    // `files:root` itself never opens.
    singleton: true,
    clickCreates: true,
    instances,
    create: () => {
      const instance = createInstance();
      layout.openInstance("files", instance.id);
    },
  };
}

export default definePlugin({
  id: "files",
  components: {
    files: defineAsyncComponent(() => import("./FilesPane.vue")),
  },
  createDockProvider,
});
