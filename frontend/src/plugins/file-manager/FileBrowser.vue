<script setup lang="ts">
/**
 * In-panel directory browser (the overlay file list): enter-style navigation
 * with a breadcrumb — clicking a directory descends into it, clicking a file
 * emits `open` so the hosting pane previews it.
 */
import { computed, inject, ref, watch } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { formatBytes } from "../../services/formatBytes";
import type { DirectoryListing, FileEntry } from "../../services/fileTypes";
import { directorySizes } from "./directorySizes";

const props = defineProps<{ dir: string }>();
const emit = defineEmits<{
  navigate: [path: string];
  open: [entry: FileEntry];
  /** The server's resolved absolute path for the requested dir ("" → cwd). */
  resolved: [path: string];
  download: [entry: FileEntry];
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

function reload(): void {
  loadedFor = null;
  void load(props.dir);
}
defineExpose({ reload });

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

function directorySize(path: string) {
  return directorySizes.state(path);
}
function directorySizeTitle(path: string): string {
  const state = directorySizes.state(path);
  if (state.status === "error") return `${state.error ?? "计算失败"}；点击重试`;
  if (state.status === "ready" && state.result !== undefined) {
    const result = state.result;
    return `${result.files} 个文件，${result.directories} 个目录${result.complete ? "" : `，${result.errors} 项读取失败（结果不完整）`}；点击重新计算`;
  }
  return state.status === "loading" ? "正在计算目录大小" : "计算目录大小（不会跟随符号链接）";
}
function calculateDirectory(path: string): void {
  void directorySizes.calculate(ctx.bus, path);
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
        <div v-for="entry in entries" :key="entry.path" class="file-entry-row">
          <button
            type="button"
            class="file-row"
            :title="entry.path"
            role="listitem"
            @click="activate(entry)"
          >
            <i class="bi entry-icon" :class="icon(entry)"></i>
            <span class="file-name">{{ entry.name }}</span>
            <span v-if="!entry.is_dir && entry.size !== null" class="file-size">{{ formatBytes(entry.size) }}</span>
          </button>
          <button
            v-if="!entry.is_dir"
            type="button"
            class="file-download"
            :title="`下载 ${entry.name}`"
            :aria-label="`下载 ${entry.name}`"
            @click="emit('download', entry)"
          >
            <i class="bi bi-download" />
          </button>
          <button
            v-if="entry.is_dir && !entry.is_symlink"
            type="button"
            class="directory-size"
            :class="{ partial: directorySize(entry.path).result?.complete === false }"
            :title="directorySizeTitle(entry.path)"
            :disabled="directorySize(entry.path).status === 'loading'"
            @click="calculateDirectory(entry.path)"
          >
            <span v-if="directorySize(entry.path).status === 'loading'" class="spinner-border spinner-border-sm" aria-label="计算中" />
            <template v-else-if="directorySize(entry.path).status === 'ready' && directorySize(entry.path).result">
              {{ directorySize(entry.path).result?.complete ? "" : "~" }}{{ formatBytes(directorySize(entry.path).result?.size ?? 0) }}
            </template>
            <template v-else-if="directorySize(entry.path).status === 'error'">重试</template>
            <template v-else>计算大小</template>
          </button>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.file-browser {
  background: var(--color-surface);
  color: var(--color-text);
  font-size: var(--font-size-ui);
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
  font-size: var(--font-size-ui-small);
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
  font-size: calc(var(--font-size-ui) * 5 / 6);
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

.file-entry-row {
  align-items: center;
  display: flex;
  min-width: 100%;
}
.file-entry-row .file-row {
  flex: 1;
  min-width: 0;
}
.file-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
.file-size {
  color: var(--color-text-subtle);
  flex: 0 0 auto;
  font-size: var(--font-size-ui-small);
  font-variant-numeric: tabular-nums;
  margin-left: 8px;
}
.directory-size {
  background: transparent;
  border: 0;
  color: var(--color-text-subtle);
  flex: 0 0 78px;
  font-size: var(--font-size-ui-small);
  font-variant-numeric: tabular-nums;
  min-height: 25px;
  padding: 0 6px;
  text-align: right;
  white-space: nowrap;
}
.directory-size:hover { color: var(--color-text); }
.directory-size.partial { color: var(--color-warning, #b58105); }
.file-download {
  background: transparent;
  border: 0;
  color: var(--color-text-subtle);
  flex: 0 0 26px;
  padding: 2px 4px;
}
.file-download:hover { color: var(--color-text); }
.entry-icon {
  color: var(--color-text-muted);
  flex: 0 0 14px;
  text-align: center;
  width: 14px;
}
</style>
