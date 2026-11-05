import { defineAsyncComponent, reactive, watch, watchEffect } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { DockInstance, DockProvider, InstanceOpenRequest, PluginInstanceAdapter } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import { hostedInstanceIds } from "../../shell/instances";
import { useLayoutStore } from "../../stores/layout";
import { basename } from "../../services/fileTypes";
import { createInstance, initInstanceStore, instances as registry, pruneUnpinned, removeInstance, updateState } from "./instanceStore";

interface ViewerParams { path?: string | null }
export function viewerAdapter(): PluginInstanceAdapter {
  return {
    type: "file-viewer",
    cardinality: "multi",
    open(request: InstanceOpenRequest): string {
      const params = (request.params ?? {}) as ViewerParams;
      if (request.instanceId !== undefined) {
        if (typeof params.path === "string" || params.path === null) {
          updateState(request.instanceId, { path: params.path }, params.path === null ? "文件预览" : basename(params.path));
        }
        return request.instanceId;
      }
      const path = typeof params.path === "string" ? params.path : null;
      return createInstance({ path }, path === null ? "文件预览" : basename(path)).id;
    },
  };
}
function createDockProvider(ctx: PluginCtx): DockProvider {
  const layout = useLayoutStore();
  const instances = reactive<DockInstance[]>([]);
  watchEffect(() => {
    instances.splice(0, instances.length, ...registry.filter((entry) => entry.pinned).map((entry) => ({
      id: entry.id, label: entry.label, icon: "bi-file-earmark-text",
    })));
  });
  const prune = (): void => pruneUnpinned(hostedInstanceIds("file-viewer"));
  watch(() => [layout.panes, layout.floating], prune, { deep: true });
  void initInstanceStore(ctx).then(prune);
  prune();
  return {
    type: "file-viewer",
    icon: "bi-file-earmark-text",
    title: "文件预览",
    instances,
    create: () => createInstance().id,
    remove: removeInstance,
  };
}
export default definePlugin({
  id: "file-viewer",
  name: "文件预览",
  components: { "file-viewer": defineAsyncComponent(() => import("./FileViewerPane.vue")) },
  createInstanceAdapters: () => [viewerAdapter()],
  createDockProvider,
});
