import { defineAsyncComponent, reactive, watch, watchEffect } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { DockInstance, DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import { useLayoutStore } from "../../stores/layout";
import {
  createInstance,
  getInstance,
  initInstanceStore,
  instances as registry,
  pruneUnpinned,
  removeInstance,
} from "./instanceStore";
import { basename } from "./types";

function openFilesIds(layout: ReturnType<typeof useLayoutStore>): Set<string> {
  const ids = new Set<string>();
  for (const pane of layout.panes) {
    if (pane.content?.paneType === "files") ids.add(pane.content.instanceId);
  }
  return ids;
}

function createDockProvider(ctx: PluginCtx): DockProvider {
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
        // A pinned folder is a launcher: clicking it spawns a fresh instance
        // rooted at that directory instead of focusing the pinned pane, so
        // the same folder can be opened any number of times. Its own pane
        // never opens, which keeps the launcher state frozen at pin time.
        clickCreates: entry.pinned && entry.state.file === null,
      })),
    );
  });

  // Unpinned instances live only while a pane hosts them; closing the pane
  // (or reloading the page) deletes them. Pinned ones stay in the Dock.
  const prune = (): void => pruneUnpinned(openFilesIds(layout));
  watch(() => layout.panes.map((pane) => pane.content), prune, { deep: true });
  // Stale unpinned leftovers from crashed sessions surface only after the
  // first server load; prune again once the registry is populated.
  void initInstanceStore(ctx).then(prune);
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
    create: (fromInstanceId?: string) => {
      // Launcher entries pass their instance id: seed the fresh instance with
      // the pinned folder's directory (label = folder name until the pane's
      // own state write refines it).
      const source = fromInstanceId !== undefined ? getInstance(fromInstanceId) : undefined;
      const dir = source !== undefined && source.state.dir !== "" ? source.state.dir : undefined;
      const instance = createInstance(
        dir !== undefined ? { dir } : undefined,
        dir !== undefined ? basename(dir) : undefined,
      );
      layout.openInstance("files", instance.id);
    },
    remove: (instanceId: string) => {
      removeInstance(instanceId);
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
