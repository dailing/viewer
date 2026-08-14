<script setup lang="ts">
import { computed, inject, onMounted, reactive, ref } from "vue";

import type { PluginCtx } from "../../shell/ctx";

type EntryType = "file" | "directory" | "symlink" | "other";

interface FileEntry {
  name: string;
  path: string;
  type: EntryType;
  size: number | null;
  mtime: number | null;
  mime: string | null;
  is_dir: boolean;
  is_symlink: boolean;
  link_target: string | null;
}

interface DirectoryListing {
  path: string;
  entries: FileEntry[];
}

interface VisibleEntry {
  entry: FileEntry;
  depth: number;
}

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("FilesPane must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const rootPath = ref("");
const rootEntries = ref<FileEntry[]>([]);
const children = reactive(new Map<string, FileEntry[]>());
const expanded = reactive(new Set<string>());
const loading = reactive(new Set<string>());
const errors = reactive(new Map<string, string>());

const visibleEntries = computed<VisibleEntry[]>(() => {
  const result: VisibleEntry[] = [];
  const append = (entries: FileEntry[], depth: number): void => {
    for (const entry of entries) {
      result.push({ entry, depth });
      if (entry.is_dir && expanded.has(entry.path)) {
        append(children.get(entry.path) ?? [], depth + 1);
      }
    }
  };
  append(rootEntries.value, 0);
  return result;
});

async function listDirectory(path: string): Promise<DirectoryListing> {
  return (await ctx.bus.request("file:_:list", { path })) as DirectoryListing;
}

async function loadRoot(): Promise<void> {
  loading.add("");
  errors.delete("");
  try {
    const listing = await listDirectory("");
    rootPath.value = listing.path;
    rootEntries.value = listing.entries;
    children.clear();
    expanded.clear();
  } catch (error) {
    rootEntries.value = [];
    errors.set("", error instanceof Error ? error.message : "无法加载目录");
  } finally {
    loading.delete("");
    // The shell's standard refresh remounts the pane, so only the root title
    // needs a plugin contribution here.
    ctx.setChrome({ title: rootPath.value || "文件" });
  }
}

async function toggle(entry: FileEntry): Promise<void> {
  if (!entry.is_dir) return;
  if (expanded.has(entry.path)) {
    expanded.delete(entry.path);
    return;
  }
  expanded.add(entry.path);
  if (children.has(entry.path) || loading.has(entry.path)) return;
  loading.add(entry.path);
  errors.delete(entry.path);
  try {
    const listing = await listDirectory(entry.path);
    children.set(entry.path, listing.entries);
  } catch (error) {
    errors.set(entry.path, error instanceof Error ? error.message : "无法加载目录");
  } finally {
    loading.delete(entry.path);
  }
}

function icon(entry: FileEntry): string {
  if (entry.is_symlink) return "bi-link-45deg";
  if (entry.is_dir) return expanded.has(entry.path) ? "bi-folder2-open" : "bi-folder2";
  if (entry.type === "file") return "bi-file-earmark";
  return "bi-question-square";
}

onMounted(() => void loadRoot());
</script>

<template>
  <div class="files-pane d-flex flex-column h-100">
    <div class="files-tree flex-grow-1 overflow-auto py-1" role="tree" aria-label="文件树">
      <div v-if="errors.has('')" class="small text-danger px-3 py-2">
        {{ errors.get("") }}
      </div>
      <div v-else-if="loading.has('')" class="small text-muted px-3 py-2">正在加载…</div>
      <div v-else-if="rootEntries.length === 0" class="small text-muted px-3 py-2">目录为空</div>
      <template v-for="row in visibleEntries" :key="`${row.depth}:${row.entry.name}:${row.entry.path}`">
        <button
          type="button"
          class="file-row"
          :class="{ expandable: row.entry.is_dir }"
          :style="{ paddingLeft: `${8 + row.depth * 16}px` }"
          :title="row.entry.path"
          role="treeitem"
          :aria-expanded="row.entry.is_dir ? expanded.has(row.entry.path) : undefined"
          @click="toggle(row.entry)"
        >
          <i
            v-if="row.entry.is_dir"
            class="bi disclosure"
            :class="expanded.has(row.entry.path) ? 'bi-chevron-down' : 'bi-chevron-right'"
          ></i>
          <span v-else class="disclosure"></span>
          <i class="bi entry-icon" :class="icon(row.entry)"></i>
          <span class="text-truncate">{{ row.entry.name }}</span>
          <span v-if="loading.has(row.entry.path)" class="spinner-border spinner-border-sm ms-auto"></span>
        </button>
        <div
          v-if="row.entry.is_dir && expanded.has(row.entry.path) && errors.has(row.entry.path)"
          class="small text-danger py-1 pe-2"
          :style="{ paddingLeft: `${40 + row.depth * 16}px` }"
        >
          {{ errors.get(row.entry.path) }}
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.files-pane {
  background: var(--color-surface);
  color: var(--color-text);
  font-size: 12px;
  min-width: 0;
}

.files-tree {
  overscroll-behavior: contain;
}

.file-row {
  align-items: center;
  background: transparent;
  border: 0;
  color: inherit;
  display: flex;
  gap: 5px;
  height: 25px;
  min-width: 100%;
  padding-bottom: 0;
  padding-right: 8px;
  padding-top: 0;
  text-align: left;
  white-space: nowrap;
}

.file-row.expandable {
  cursor: pointer;
}

.file-row:not(.expandable) {
  cursor: default;
}

.file-row:hover {
  background: var(--color-surface-hover);
}

.disclosure {
  flex: 0 0 11px;
  font-size: 9px;
  text-align: center;
  width: 11px;
}

.entry-icon {
  color: var(--color-text-muted);
  flex: 0 0 14px;
  text-align: center;
  width: 14px;
}

.spinner-border-sm {
  height: 10px;
  width: 10px;
}
</style>
