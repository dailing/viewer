<script setup lang="ts">
/**
 * In-panel directory browser (the overlay file list): enter-style navigation
 * with a breadcrumb — clicking a directory descends into it, clicking a file
 * emits `open` so the hosting pane previews it.
 */
import { computed, inject, ref, watch } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { DirectoryListing, FileEntry } from "./types";

const props = defineProps<{ dir: string }>();
const emit = defineEmits<{
  navigate: [path: string];
  open: [entry: FileEntry];
  /** The server's resolved absolute path for the requested dir ("" → cwd). */
  resolved: [path: string];
}>();

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("FileBrowser must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const entries = ref<FileEntry[]>([]);
const current = ref("");
const loading = ref(false);
const error = ref<string | null>(null);
/** Last path actually fetched — guards the resolve round-trip ("" → abs). */
let loadedFor: string | null = null;

interface Crumb {
  name: string;
  path: string;
}

const parentDir = computed<string | null>(() => {
  const path = current.value;
  if (path === "" || path === "/") return null;
  const trimmed = path.endsWith("/") ? path.slice(0, -1) : path;
  const index = trimmed.lastIndexOf("/");
  return index <= 0 ? "/" : trimmed.slice(0, index);
});

const crumbs = computed<Crumb[]>(() => {
  const path = current.value;
  if (path === "") return [];
  const segments = path.split("/").filter((segment) => segment.length > 0);
  const result: Crumb[] = [{ name: "/", path: "/" }];
  let acc = "";
  for (const segment of segments) {
    acc += `/${segment}`;
    result.push({ name: segment, path: acc });
  }
  return result;
});

async function load(path: string): Promise<void> {
  if (path === loadedFor) return;
  loading.value = true;
  error.value = null;
  try {
    const listing = (await ctx.bus.request("file:_:list", { path })) as DirectoryListing;
    entries.value = listing.entries;
    current.value = listing.path;
    loadedFor = path;
    if (listing.path !== path) {
      loadedFor = listing.path;
      emit("resolved", listing.path);
    }
  } catch (cause) {
    entries.value = [];
    error.value = cause instanceof Error ? cause.message : "无法加载目录";
  } finally {
    loading.value = false;
  }
}

watch(() => props.dir, (dir) => void load(dir), { immediate: true });

function activate(entry: FileEntry): void {
  if (entry.is_dir) emit("navigate", entry.path);
  else emit("open", entry);
}

function icon(entry: FileEntry): string {
  if (entry.is_symlink) return "bi-link-45deg";
  if (entry.is_dir) return "bi-folder2";
  if (entry.type === "file") return "bi-file-earmark";
  return "bi-question-square";
}
</script>

<template>
  <div class="file-browser d-flex flex-column h-100">
    <nav class="file-crumbs" aria-label="路径">
      <template v-for="(crumb, index) in crumbs" :key="crumb.path">
        <span v-if="index > 0" class="crumb-sep">/</span>
        <button
          type="button"
          class="crumb"
          :class="{ current: index === crumbs.length - 1 }"
          :title="crumb.path"
          @click="emit('navigate', crumb.path)"
        >
          {{ crumb.name }}
        </button>
      </template>
      <span v-if="crumbs.length === 0" class="crumb current">…</span>
    </nav>
    <div class="file-list flex-grow-1 overflow-auto py-1" role="list" aria-label="文件列表">
      <div v-if="error !== null" class="small text-danger px-3 py-2">{{ error }}</div>
      <div v-else-if="loading" class="small text-muted px-3 py-2">正在加载…</div>
      <template v-else>
        <button
          v-if="parentDir !== null"
          type="button"
          class="file-row"
          :title="parentDir"
          role="listitem"
          @click="emit('navigate', parentDir)"
        >
          <i class="bi entry-icon bi-arrow-90deg-up"></i>
          <span class="text-truncate">..</span>
        </button>
        <div v-if="entries.length === 0" class="small text-muted px-3 py-2">目录为空</div>
        <button
          v-for="entry in entries"
          :key="entry.path"
          type="button"
          class="file-row"
          :title="entry.path"
          role="listitem"
          @click="activate(entry)"
        >
          <i class="bi entry-icon" :class="icon(entry)"></i>
          <span class="text-truncate">{{ entry.name }}</span>
        </button>
      </template>
    </div>
  </div>
</template>

<style scoped>
.file-browser {
  background: var(--color-surface);
  color: var(--color-text);
  font-size: 12px;
  min-width: 0;
}

.file-crumbs {
  align-items: center;
  border-bottom: 1px solid var(--color-border);
  display: flex;
  flex: 0 0 auto;
  gap: 1px;
  min-width: 0;
  overflow-x: auto;
  padding: 4px 8px;
  white-space: nowrap;
}

.crumb {
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  flex: 0 0 auto;
  font-size: 11px;
  max-width: 140px;
  overflow: hidden;
  padding: 1px 3px;
  text-overflow: ellipsis;
}

.crumb:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.crumb.current {
  color: var(--color-text);
}

.crumb-sep {
  color: var(--color-text-subtle);
  flex: 0 0 auto;
  font-size: 10px;
}

.file-list {
  overscroll-behavior: contain;
}

.file-row {
  align-items: center;
  background: transparent;
  border: 0;
  color: inherit;
  cursor: pointer;
  display: flex;
  gap: 5px;
  height: 25px;
  min-width: 100%;
  padding: 0 8px;
  text-align: left;
  white-space: nowrap;
}

.file-row:hover {
  background: var(--color-surface-hover);
}

.entry-icon {
  color: var(--color-text-muted);
  flex: 0 0 14px;
  text-align: center;
  width: 14px;
}
</style>
