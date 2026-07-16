<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { getTextLines } from "../../api/client";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { restoreScrollPosition, saveScrollPosition } from "../../utils/scrollMemory";

const props = defineProps<{
  path: string;
  version: number;
  paneId: string;
  workspaceId: string;
  kind: "text" | "markdown" | "csv" | "html";
  size: number;
}>();

const toolbar = usePaneToolbarStore();
const container = ref<HTMLElement | null>(null);
const lines = ref<string[]>([]);
const loadedStart = ref(0);
const totalLines = ref(1);
const error = ref("");
const loading = ref(false);
const lineHeight = 20;
const overscan = 80;
const maxRequestLines = 500;
const maxScrollHeight = 20_000_000;
let requestSerial = 0;
let skipUnmountSave = false;

const naturalHeight = computed(() => Math.max(totalLines.value, 1) * lineHeight);
const totalHeight = computed(() => Math.min(naturalHeight.value, maxScrollHeight));
const scrollLineHeight = computed(() => totalHeight.value / Math.max(totalLines.value, 1));
const offsetTop = computed(() => loadedStart.value * scrollLineHeight.value);
const fileSizeLabel = computed(() => formatBytes(props.size));
const loadedEnd = computed(() => loadedStart.value + lines.value.length);

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let size = value / 1024;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }
  return `${size.toFixed(size >= 10 ? 1 : 2)} ${units[unitIndex]}`;
}

function target() {
  return { path: props.path, paneId: props.paneId, workspaceId: props.workspaceId };
}

function saveCurrentScroll() {
  saveScrollPosition(target(), container.value);
}

function lineForScroll(): number {
  const physicalLineHeight = scrollLineHeight.value || lineHeight;
  return Math.max(0, Math.floor((container.value?.scrollTop ?? 0) / physicalLineHeight));
}

function requestStartFor(line: number): number {
  return Math.max(0, line - overscan);
}

function requestCount(): number {
  const viewportLines = Math.ceil((container.value?.clientHeight || 600) / lineHeight);
  return Math.min(maxRequestLines, Math.max(120, viewportLines + overscan * 2));
}

function registerToolbar() {
  toolbar.setPaneToolbar(props.paneId, {
    title: props.path,
    status: `Large ${props.kind} · ${fileSizeLabel.value} · ${totalLines.value.toLocaleString()} lines`,
    actions: [
      { id: "large-text-top", title: "Go to top", icon: "bi-arrow-up", run: goTop },
      { id: "large-text-end", title: "Go to end", icon: "bi-arrow-down", run: goEnd },
      { id: "large-text-copy-window", title: "Copy visible window", icon: "bi-clipboard", run: copyWindow },
    ],
  });
}

async function loadAround(line: number, force = false) {
  const requestedStart = requestStartFor(line);
  const requestedEnd = requestedStart + requestCount();
  if (!force && lines.value.length && requestedStart >= loadedStart.value && requestedEnd <= loadedEnd.value) {
    return;
  }

  const serial = ++requestSerial;
  loading.value = true;
  error.value = "";
  try {
    const window = await getTextLines(props.path, requestedStart, requestCount());
    if (serial !== requestSerial) return;
    loadedStart.value = window.start_line;
    lines.value = window.lines;
    totalLines.value = Math.max(window.total_lines, 1);
    registerToolbar();
  } catch (err) {
    if (serial !== requestSerial) return;
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (serial === requestSerial) loading.value = false;
  }
}

function handleScroll() {
  saveCurrentScroll();
  void loadAround(lineForScroll());
}

function goTop() {
  if (!container.value) return;
  container.value.scrollTop = 0;
  void loadAround(0, true);
}

function goEnd() {
  if (!container.value) return;
  const lastLine = Math.max(totalLines.value - 1, 0);
  container.value.scrollTop = Math.max(0, lastLine * scrollLineHeight.value - container.value.clientHeight + lineHeight);
  void loadAround(lastLine, true);
}

async function copyWindow() {
  await navigator.clipboard.writeText(lines.value.join("\n"));
}

async function initialLoad() {
  await loadAround(0, true);
  await nextTick();
  await restoreScrollPosition(target(), container.value);
  await loadAround(lineForScroll(), true);
}

function saveBeforePaneNavigate(event: Event) {
  const targetPaneId = (event as CustomEvent<{ paneId?: string }>).detail?.paneId;
  if (targetPaneId === props.paneId) {
    saveCurrentScroll();
    skipUnmountSave = true;
  }
}

function saveBeforeWorkspaceSwitch() {
  saveCurrentScroll();
  skipUnmountSave = true;
}

watch(
  () => [props.path, props.version] as const,
  async ([newPath], [oldPath, oldVersion]) => {
    if (oldPath && newPath !== oldPath) saveCurrentScroll();
    else if (oldVersion !== undefined) saveCurrentScroll();
    skipUnmountSave = false;
    lines.value = [];
    loadedStart.value = 0;
    totalLines.value = 1;
    await initialLoad();
  },
);

onMounted(() => {
  window.addEventListener("beforeunload", saveCurrentScroll);
  window.addEventListener("viewer:pane-before-navigate", saveBeforePaneNavigate);
  window.addEventListener("viewer:workspace-before-switch", saveBeforeWorkspaceSwitch);
  registerToolbar();
  void initialLoad();
});

onUnmounted(() => {
  if (!skipUnmountSave) saveCurrentScroll();
  toolbar.clearPaneToolbar(props.paneId);
  window.removeEventListener("beforeunload", saveCurrentScroll);
  window.removeEventListener("viewer:pane-before-navigate", saveBeforePaneNavigate);
  window.removeEventListener("viewer:workspace-before-switch", saveBeforeWorkspaceSwitch);
});
</script>

<template>
  <div class="large-text-viewer">
    <div v-if="error" class="large-text-error">{{ error }}</div>
    <div v-else ref="container" class="large-text-scroll" @scroll.passive="handleScroll">
      <div class="large-text-spacer" :style="{ height: `${totalHeight}px` }">
        <pre class="large-text-window" :style="{ transform: `translateY(${offsetTop}px)` }"><code><span
          v-for="(line, index) in lines"
          :key="loadedStart + index"
          class="large-text-line"
        ><span class="large-text-gutter">{{ loadedStart + index + 1 }}</span><span class="large-text-content">{{ line || " " }}</span></span></code></pre>
      </div>
      <div v-if="loading" class="large-text-loading">Loading</div>
    </div>
  </div>
</template>

<style scoped>
.large-text-viewer {
  background: var(--syntax-background);
  color: var(--syntax-text);
  height: 100%;
  min-height: 0;
  position: relative;
}

.large-text-scroll {
  height: 100%;
  overflow: auto;
  position: relative;
}

.large-text-spacer {
  min-width: 100%;
  position: relative;
}

.large-text-window {
  font-family: var(--bs-font-monospace);
  font-size: 13px;
  left: 0;
  line-height: 20px;
  margin: 0;
  min-width: 100%;
  padding: 0;
  position: absolute;
  top: 0;
  white-space: pre;
}

.large-text-line {
  display: grid;
  grid-template-columns: 72px minmax(0, 1fr);
  min-height: 20px;
}

.large-text-gutter {
  background: color-mix(in srgb, var(--syntax-background) 88%, var(--color-text-muted));
  border-right: 1px solid var(--color-border);
  color: var(--color-text-muted);
  padding: 0 8px;
  position: sticky;
  left: 0;
  text-align: right;
  user-select: none;
}

.large-text-content {
  padding: 0 14px 0 10px;
  tab-size: 2;
}

.large-text-loading {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  font-size: 12px;
  padding: 4px 8px;
  position: absolute;
  right: 12px;
  top: 10px;
}

.large-text-error {
  color: var(--color-danger);
  padding: 14px;
}
</style>
