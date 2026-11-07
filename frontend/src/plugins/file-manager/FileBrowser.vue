<script setup lang="ts">
/**
 * In-panel directory browser (the overlay file list): enter-style navigation
 * with a breadcrumb — clicking a directory descends into it, clicking a file
 * emits `open` so the hosting pane previews it.
 */
import { computed, inject, onBeforeUnmount, ref, watch } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { bus as shellBus } from "../../shell/bus";
import { formatBytes } from "../../services/formatBytes";
import type { DirectoryListing, FileEntry } from "../../services/fileTypes";
import { dirname } from "../../services/fileTypes";
import { directorySizes } from "./directorySizes";
import FolderTree from "./FolderTree.vue";

const props = defineProps<{ dir: string; showHidden: boolean }>();
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
const loadingMore = ref(false);
const hasMore = ref(false);
const nextOffset = ref(0);
const loadMoreError = ref<string | null>(null);
const error = ref<string | null>(null);
const showTree = ref(true);
const treeWidth = ref(190);
const browserBodyRef = ref<HTMLElement | null>(null);
const treeRef = ref<InstanceType<typeof FolderTree> | null>(null);
const deleting = ref<string | null>(null);
const creating = ref(false);
/** Last path actually fetched — guards the resolve round-trip ("" → abs). */
let loadedFor: string | null = null;
let loadGeneration = 0;
const directoryPageSize = 100;
let stopTreeResize: (() => void) | null = null;

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

function queueDirectorySizes(page: FileEntry[]): void {
  // The global bus subscription is shared by all manager instances and is not
  // disposed when the pane that initiated sizing closes.
  for (const entry of page) {
    if (entry.is_dir && !entry.is_symlink) void directorySizes.ensure(shellBus, entry.path);
  }
}

async function load(path: string): Promise<void> {
  if (path === loadedFor) return;
  const generation = ++loadGeneration;
  loading.value = true;
  loadingMore.value = false;
  hasMore.value = false;
  loadMoreError.value = null;
  error.value = null;
  try {
    const listing = (await ctx.bus.request("file:_:list", {
      path,
      offset: 0,
      limit: directoryPageSize,
      show_hidden: props.showHidden,
    })) as DirectoryListing;
    if (generation !== loadGeneration) return;
    entries.value = listing.entries;
    hasMore.value = listing.has_more === true;
    nextOffset.value = listing.next_offset ?? listing.entries.length;
    current.value = listing.path;
    loadedFor = path;
    if (listing.path !== path) {
      loadedFor = listing.path;
      emit("resolved", listing.path);
    }
    queueDirectorySizes(listing.entries);
  } catch (cause) {
    if (generation !== loadGeneration) return;
    entries.value = [];
    error.value = cause instanceof Error ? cause.message : "无法加载目录";
  } finally {
    if (generation === loadGeneration) loading.value = false;
  }
}

async function loadMore(): Promise<void> {
  if (loading.value || loadingMore.value || !hasMore.value) return;
  const generation = loadGeneration;
  const path = current.value;
  loadingMore.value = true;
  loadMoreError.value = null;
  try {
    const listing = (await ctx.bus.request("file:_:list", {
      path,
      offset: nextOffset.value,
      limit: directoryPageSize,
      show_hidden: props.showHidden,
    })) as DirectoryListing;
    if (generation !== loadGeneration || path !== current.value) return;
    const known = new Set(entries.value.map((entry) => entry.path));
    const fresh = listing.entries.filter((entry) => !known.has(entry.path));
    entries.value = [...entries.value, ...fresh];
    hasMore.value = listing.has_more === true;
    nextOffset.value = listing.next_offset ?? nextOffset.value + listing.entries.length;
    queueDirectorySizes(fresh);
  } catch (cause) {
    if (generation === loadGeneration) loadMoreError.value = cause instanceof Error ? cause.message : "无法继续加载目录";
  } finally {
    if (generation === loadGeneration) loadingMore.value = false;
  }
}

function handleListScroll(event: Event): void {
  const list = event.currentTarget as HTMLElement;
  if (list.scrollHeight - list.scrollTop - list.clientHeight < 180) void loadMore();
}

function reload(): void {
  loadedFor = null;
  void load(current.value || props.dir);
  if (current.value !== "") void treeRef.value?.refresh(current.value);
}

async function createFolder(): Promise<void> {
  if (creating.value) return;
  const name = prompt("新建文件夹名称")?.trim();
  if (!name) return;
  creating.value = true;
  error.value = null;
  try {
    await ctx.bus.request("file:_:mkdir", { dir: current.value || props.dir, name });
    reload();
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "新建文件夹失败";
  } finally {
    creating.value = false;
  }
}

async function removeEntry(entry: FileEntry): Promise<void> {
  if (deleting.value !== null) return;
  const recursive = entry.is_dir && !entry.is_symlink;
  const warning = recursive
    ? `确定删除文件夹“${entry.name}”及其全部内容吗？此操作不可撤销。`
    : `确定删除“${entry.name}”吗？此操作不可撤销。`;
  if (!confirm(warning)) return;
  deleting.value = entry.path;
  error.value = null;
  try {
    await ctx.bus.request("file:_:delete", { path: entry.path });
    const parent = dirname(entry.path) || "/";
    const insideDeleted = current.value === entry.path || current.value.startsWith(`${entry.path}/`);
    if (insideDeleted) emit("navigate", parent);
    loadedFor = null;
    await load(insideDeleted ? parent : current.value || props.dir);
    void treeRef.value?.refresh(parent);
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "删除失败";
  } finally {
    deleting.value = null;
  }
}
defineExpose({ reload, createFolder });

watch(() => props.dir, (dir) => void load(dir), { immediate: true });
watch(() => props.showHidden, () => {
  loadedFor = null;
  void load(current.value || props.dir);
});

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
  if (state.result !== undefined) {
    const result = state.result;
    const suffix = state.status === "queued" || state.status === "loading" ? "；点击取消" : "；点击重新计算";
    return `${result.files} 个文件，${result.directories} 个目录${result.complete ? "" : `，${result.errors} 项读取失败或仍在统计（结果不完整）`}${suffix}`;
  }
  if (state.status === "queued") return "排队等待后台统计；点击取消";
  if (state.status === "loading") return "正在后台分片统计；点击取消";
  return "计算目录大小（不会跟随符号链接）";
}
function calculateDirectory(path: string): void {
  const status = directorySizes.state(path).status;
  if (status === "queued" || status === "loading") void directorySizes.cancel(shellBus, path);
  else void directorySizes.calculate(shellBus, path);
}

function clampTreeWidth(width: number): number {
  const bodyWidth = browserBodyRef.value?.clientWidth ?? 600;
  return Math.max(120, Math.min(560, Math.min(width, Math.max(120, bodyWidth - 160))));
}
function beginTreeResize(event: PointerEvent): void {
  event.preventDefault();
  stopTreeResize?.();
  const startX = event.clientX;
  const startWidth = treeWidth.value;
  const move = (pointer: PointerEvent): void => {
    treeWidth.value = clampTreeWidth(startWidth + pointer.clientX - startX);
  };
  const stop = (): void => {
    window.removeEventListener("pointermove", move);
    window.removeEventListener("pointerup", stop);
    window.removeEventListener("pointercancel", stop);
    if (stopTreeResize === stop) stopTreeResize = null;
  };
  stopTreeResize = stop;
  window.addEventListener("pointermove", move);
  window.addEventListener("pointerup", stop);
  window.addEventListener("pointercancel", stop);
}
function adjustTreeWidth(delta: number): void {
  treeWidth.value = clampTreeWidth(treeWidth.value + delta);
}
onBeforeUnmount(() => stopTreeResize?.());
</script>

<template>
  <div class="file-browser d-flex flex-column h-100">
    <nav class="file-crumbs" aria-label="路径">
      <button
        type="button"
        class="tree-switch"
        :title="showTree ? '收起文件夹树' : '显示文件夹树'"
        :aria-label="showTree ? '收起文件夹树' : '显示文件夹树'"
        @click="showTree = !showTree"
      >
        <i class="bi bi-diagram-3" />
      </button>
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
    <div ref="browserBodyRef" class="browser-body">
      <FolderTree
        v-if="showTree"
        :key="showHidden ? 'show-hidden' : 'hide-hidden'"
        ref="treeRef"
        :current="current"
        :show-hidden="showHidden"
        :style="{ flexBasis: `${treeWidth}px` }"
        @navigate="emit('navigate', $event)"
      />
      <div
        v-if="showTree"
        class="tree-resizer"
        role="separator"
        aria-label="调整文件夹树宽度"
        aria-orientation="vertical"
        :aria-valuenow="treeWidth"
        tabindex="0"
        @pointerdown="beginTreeResize"
        @keydown.left.prevent="adjustTreeWidth(-16)"
        @keydown.right.prevent="adjustTreeWidth(16)"
      />
      <div class="file-list flex-grow-1 overflow-auto py-1" role="list" aria-label="文件列表" @scroll.passive="handleListScroll">
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
            type="button"
            class="file-delete"
            :title="entry.is_dir && !entry.is_symlink ? `删除文件夹 ${entry.name} 及其全部内容` : `删除 ${entry.name}`"
            :aria-label="`删除 ${entry.name}`"
            :disabled="deleting !== null"
            @click="removeEntry(entry)"
          >
            <span v-if="deleting === entry.path" class="spinner-border spinner-border-sm" />
            <i v-else class="bi bi-trash" />
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
            @click="calculateDirectory(entry.path)"
          >
            <span v-if="directorySize(entry.path).status === 'queued' || directorySize(entry.path).status === 'loading'" class="spinner-border spinner-border-sm" aria-label="统计中" />
            <template v-if="directorySize(entry.path).result !== undefined">
              {{ directorySize(entry.path).result?.complete ? "" : "~" }}{{ formatBytes(directorySize(entry.path).result?.size ?? 0) }}
            </template>
            <template v-else-if="directorySize(entry.path).status === 'queued'">排队</template>
            <template v-else-if="directorySize(entry.path).status === 'error'">重试</template>
            <template v-else>计算大小</template>
          </button>
        </div>
        <div v-if="hasMore || loadingMore || loadMoreError !== null" class="directory-more">
          <span v-if="loadMoreError !== null" class="text-danger">{{ loadMoreError }}</span>
          <button type="button" :disabled="loadingMore" @click="loadMore">
            <span v-if="loadingMore" class="spinner-border spinner-border-sm" />
            {{ loadingMore ? "正在加载…" : loadMoreError !== null ? "重试加载更多" : "加载更多" }}
          </button>
        </div>
      </template>
      </div>
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
.browser-body {
  display: flex;
  flex: 1;
  min-height: 0;
  min-width: 0;
}
.tree-resizer {
  background: transparent;
  cursor: col-resize;
  flex: 0 0 5px;
  margin-left: -2px;
  position: relative;
  touch-action: none;
  z-index: 2;
}
.tree-resizer:hover,
.tree-resizer:focus { background: color-mix(in srgb, var(--color-accent) 35%, transparent); outline: none; }
.directory-more {
  align-items: center;
  color: var(--color-text-muted);
  display: flex;
  font-size: var(--font-size-ui-small);
  gap: 8px;
  justify-content: center;
  min-height: 30px;
  padding: 3px 8px;
}
.directory-more button {
  background: transparent;
  border: 0;
  color: var(--color-text-muted);
  padding: 3px 8px;
}
.directory-more button:hover:not(:disabled) { color: var(--color-text); }
.directory-more .spinner-border { height: 10px; width: 10px; }
.tree-switch {
  background: transparent;
  border: 0;
  color: var(--color-text-subtle);
  flex: 0 0 24px;
  padding: 1px 4px;
}
.tree-switch:hover { color: var(--color-text); }

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
.file-download,
.file-delete {
  background: transparent;
  border: 0;
  color: var(--color-text-subtle);
  flex: 0 0 26px;
  padding: 2px 4px;
}
.file-download:hover,
.file-delete:hover { color: var(--color-text); }
.file-delete:disabled { opacity: 0.45; }
.entry-icon {
  color: var(--color-text-muted);
  flex: 0 0 14px;
  text-align: center;
  width: 14px;
}
</style>
