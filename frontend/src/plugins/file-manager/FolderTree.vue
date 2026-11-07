<script setup lang="ts">
import { computed, inject, ref, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import { formatBytes } from "../../services/formatBytes";
import { directorySizes } from "./directorySizes";
import { DirectoryTreeModel, type DirectoryTreeNode } from "./directoryTree";

const props = defineProps<{ current: string; showHidden: boolean }>();
const emit = defineEmits<{ navigate: [path: string] }>();
const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("FolderTree must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;
const model = new DirectoryTreeModel(props.showHidden);
const revealing = ref(false);

interface TreeRow { key: string; node: DirectoryTreeNode; depth: number; loadMore: boolean }
const rows = computed<TreeRow[]>(() => {
  const result: TreeRow[] = [];
  const append = (path: string, depth: number): void => {
    const node = model.nodes.get(path);
    if (node === undefined) return;
    result.push({ key: path, node, depth, loadMore: false });
    if (node.expanded) {
      for (const child of node.children) append(child, depth + 1);
      if (node.hasMore) result.push({ key: `${path}:more`, node, depth: depth + 1, loadMore: true });
    }
  };
  append("/", 0);
  return result;
});

watch(() => props.current, (current) => {
  void reveal(current || "/");
}, { immediate: true });

async function reveal(path: string): Promise<void> {
  revealing.value = true;
  try {
    await model.reveal(ctx.bus, path);
  } finally {
    revealing.value = false;
  }
}
async function toggle(path: string): Promise<void> {
  await model.toggle(ctx.bus, path);
}
async function loadMore(path: string): Promise<void> {
  await model.loadMore(ctx.bus, path);
}
async function refresh(path: string): Promise<void> {
  await model.invalidate(ctx.bus, path);
  if (props.current === path || props.current.startsWith(`${path}/`)) await reveal(props.current);
}
defineExpose({ refresh });
function sizeLabel(path: string): string {
  const state = directorySizes.state(path);
  if (state.result === undefined) return "";
  return `${state.status === "ready" && state.result.complete ? "" : "~"}${formatBytes(state.result.size)}`;
}
</script>

<template>
  <aside class="folder-tree" aria-label="文件夹树">
    <div class="tree-heading">文件夹</div>
    <div class="tree-nodes">
      <div v-for="row in rows" :key="row.key" class="tree-row" :style="{ paddingLeft: `${row.depth * 12}px` }">
        <button
          v-if="row.loadMore"
          type="button"
          class="tree-load-more"
          :disabled="row.node.loading"
          @click="loadMore(row.node.path)"
        >
          <span v-if="row.node.loading" class="spinner-border spinner-border-sm" />
          {{ row.node.loading ? "正在加载…" : "加载更多子目录" }}
        </button>
        <template v-else>
        <button
          type="button"
          class="tree-toggle"
          :title="row.node.expanded ? '收起' : '展开'"
          :aria-label="`${row.node.expanded ? '收起' : '展开'} ${row.node.name}`"
          @click="toggle(row.node.path)"
        >
          <span v-if="row.node.loading" class="spinner-border spinner-border-sm" />
          <i v-else class="bi" :class="row.node.expanded ? 'bi-chevron-down' : 'bi-chevron-right'" />
        </button>
        <button
          type="button"
          class="tree-name"
          :class="{ current: current === row.node.path }"
          :title="row.node.error ? `${row.node.path}\n${row.node.error}` : row.node.path"
          @click="emit('navigate', row.node.path)"
        >
          <i class="bi" :class="row.node.expanded ? 'bi-folder2-open' : 'bi-folder2'" />
          <span>{{ row.node.name }}</span>
        </button>
        <span v-if="sizeLabel(row.node.path)" class="tree-size">{{ sizeLabel(row.node.path) }}</span>
        </template>
      </div>
    </div>
  </aside>
</template>

<style scoped>
.folder-tree {
  border-right: 1px solid var(--color-border);
  display: flex;
  flex: 0 0 190px;
  flex-direction: column;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
}
.tree-heading {
  color: var(--color-text-subtle);
  flex: 0 0 auto;
  font-size: var(--font-size-ui-small);
  padding: 5px 9px 3px;
}
.tree-nodes { flex: 1; min-height: 0; overflow: auto; overscroll-behavior: contain; padding-bottom: 5px; }
.tree-row { align-items: center; display: flex; min-width: 0; }
.tree-toggle {
  align-items: center;
  background: transparent;
  border: 0;
  color: var(--color-text-subtle);
  display: flex;
  flex: 0 0 24px;
  justify-content: center;
  min-height: 24px;
  padding-right: 0;
}
.tree-toggle .spinner-border { height: 10px; width: 10px; }
.tree-name {
  align-items: center;
  background: transparent;
  border: 0;
  color: var(--color-text-muted);
  display: flex;
  flex: 1;
  gap: 5px;
  min-height: 24px;
  min-width: 0;
  padding: 1px 3px;
  text-align: left;
}
.tree-name span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tree-name:hover { background: var(--color-surface-hover); color: var(--color-text); }
.tree-name.current { color: var(--color-text); }
.tree-size { color: var(--color-text-subtle); flex: 0 0 auto; font-size: var(--font-size-ui-small); padding-right: 6px; }
.tree-load-more {
  background: transparent;
  border: 0;
  color: var(--color-text-subtle);
  flex: 1;
  font-size: var(--font-size-ui-small);
  min-height: 24px;
  padding: 1px 6px 1px 28px;
  text-align: left;
}
.tree-load-more:hover:not(:disabled) { background: var(--color-surface-hover); color: var(--color-text); }
.tree-load-more .spinner-border { height: 10px; width: 10px; }
</style>
