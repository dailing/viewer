import { defineAsyncComponent, reactive, watch, watchEffect } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { DockInstance, DockProvider, InstanceOpenRequest, PluginInstanceAdapter } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import { hostedInstanceIds } from "../../shell/instances";
import { useLayoutStore } from "../../stores/layout";
import { basename } from "../../services/fileTypes";
import { createInstance, initInstanceStore, instances as registry, pruneUnpinned, removeInstance, updateState } from "./instanceStore";

interface ManagerParams { dir?: string }
export function managerAdapter(): PluginInstanceAdapter {
  return {
    type: "file-manager",
    cardinality: "multi",
    open(request: InstanceOpenRequest): string {
      const params = (request.params ?? {}) as ManagerParams;
      if (request.instanceId !== undefined) {
        if (typeof params.dir === "string") {
          updateState(request.instanceId, { dir: params.dir }, params.dir === "" ? "文件管理器" : basename(params.dir));
        }
        return request.instanceId;
      }
      const dir = typeof params.dir === "string" ? params.dir : "";
      return createInstance({ dir }, dir === "" ? "文件管理器" : basename(dir)).id;
    },
  };
}

function createDockProvider(ctx: PluginCtx): DockProvider {
  const layout = useLayoutStore();
  const instances = reactive<DockInstance[]>([]);
  watchEffect(() => {
    instances.splice(0, instances.length, ...registry.filter((entry) => entry.pinned).map((entry) => ({
      id: entry.id, label: entry.label, icon: "bi-folder2",
    })));
  });
  const prune = (): void => pruneUnpinned(hostedInstanceIds("file-manager"));
  watch(() => [layout.panes, layout.floating], prune, { deep: true });
  void initInstanceStore(ctx).then(prune);
  prune();
  return {
    type: "file-manager",
    icon: "bi-folder2-open",
    title: "文件管理器",
    instances,
    create: () => createInstance().id,
    remove: removeInstance,
  };
}

export default definePlugin({
  id: "file-manager",
  name: "文件管理器",
  components: { "file-manager": defineAsyncComponent(() => import("./FileManagerPane.vue")) },
  createInstanceAdapters: () => [managerAdapter()],
  createDockProvider,
});
