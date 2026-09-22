<script setup lang="ts">
/**
 * Files pane: one virtual instance = one file-preview browser. The overlay
 * file list (FileBrowser, toggled by the title-bar action) navigates
 * directories and picks files; FilePreview shows the picked file. Per-instance
 * view state and the Dock label persist server-side via instanceStore; the
 * pin action in the pane chrome keeps the instance alive after its pane closes.
 */
import { computed, inject, ref, watch, watchEffect } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { PaneChromeAction } from "../../stores/paneChrome";
import FileBrowser from "./FileBrowser.vue";
import FilePreview from "./FilePreview.vue";
import { getInstance, setPinned, updateState } from "./instanceStore";
import type { FilesViewState, PreviewMode } from "./instanceStore";
import { basename, dirname, kindForPath } from "./types";
import type { FileEntry } from "./types";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("FilesPane must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;
const instanceId = ctx.instanceId;
// Panes are only opened through the dock provider, which registers the
// instance first; the fallbacks keep a stale layout from crashing the pane.
// The record may also arrive late (server list still loading after a reload),
// so it is a computed and its state is adopted once when it first appears.
const record = computed(() => getInstance(instanceId));

// The tree's default view follows the open file's directory; only folder
// instances (no file) use the persisted browsing directory as-is.
function dirForState(state: FilesViewState): string {
  return state.file !== null ? dirname(state.file) : state.dir;
}

const dir = ref("");
const file = ref<string | null>(null);
const mode = ref<PreviewMode>("render");
const overlayOpen = ref(true);
/** PDF margin crop per axis (0–100, 100 = trim to content). */
const trimX = ref(100);
const trimY = ref(100);
/** PDF theme mapping: pages render in the theme's canvas/text colors. */
const themeMap = ref(true);
/** Slider drafts: dragging must not re-render server-side, so the committed
 * values update only on slider release (change event). */
const draftTrimX = ref(100);
const draftTrimY = ref(100);
const trimOpen = ref(false);
/** Bumped by the title-bar refresh action; FilePreview re-reads the file. */
const reloadTick = ref(0);
const pinned = computed(() => record.value?.pinned ?? false);

function adoptState(state: FilesViewState): void {
  dir.value = dirForState(state);
  file.value = state.file;
  mode.value = state.mode;
  overlayOpen.value = state.overlayOpen;
  trimX.value = state.trimX;
  trimY.value = state.trimY;
  themeMap.value = state.themeMap;
}

function commitTrim(): void {
  trimX.value = draftTrimX.value;
  trimY.value = draftTrimY.value;
}

let adopted = record.value !== undefined;
if (record.value !== undefined) adoptState(record.value.state);
watch(record, (entry) => {
  // Adopt once: a record that disappears and returns (pruned by another
  // browser, then recreated by this pane's own writes) must not clobber
  // the pane's live view state.
  if (adopted || entry === undefined) return;
  adopted = true;
  adoptState(entry.state);
});

const previewKind = computed(() => (file.value === null ? null : kindForPath(file.value)));
const label = computed(() => {
  if (file.value !== null) return basename(file.value);
  if (dir.value !== "" && dir.value !== "/") return basename(dir.value);
  return "文件";
});

function openFile(entry: FileEntry): void {
  file.value = entry.path;
  // The tree follows the open file so it always shows the file's directory.
  dir.value = dirname(entry.path);
}

watch([dir, file, mode, overlayOpen, trimX, trimY, themeMap], () => {
  updateState(
    instanceId,
    {
      dir: dir.value,
      file: file.value,
      mode: mode.value,
      overlayOpen: overlayOpen.value,
      trimX: trimX.value,
      trimY: trimY.value,
      themeMap: themeMap.value,
    },
    label.value,
  );
});

watchEffect(() => {
  const actions: PaneChromeAction[] = [
    {
      id: "toggle-overlay",
      title: overlayOpen.value ? "隐藏文件列表" : "显示文件列表",
      icon: "bi-folder2-open",
      active: overlayOpen.value,
      run: () => {
        overlayOpen.value = !overlayOpen.value;
      },
    },
  ];
  if (file.value !== null) {
    actions.push({
      id: "refresh",
      title: "刷新（重新读取文件）",
      icon: "bi-arrow-clockwise",
      run: () => {
        reloadTick.value += 1;
      },
    });
  }
  if (previewKind.value === "pdf") {
    actions.push({
      id: "trim",
      title: `页边距裁切（横向 ${trimX.value}% / 纵向 ${trimY.value}%）`,
      icon: "bi-crop",
      active: trimOpen.value,
      run: () => {
        trimOpen.value = !trimOpen.value;
        if (trimOpen.value) {
          draftTrimX.value = trimX.value;
          draftTrimY.value = trimY.value;
        }
      },
    });
    actions.push({
      id: "theme-map",
      title: themeMap.value ? "跟随主题配色：开（页面随主题背景/文字色）" : "跟随主题配色：关（原始白底黑字）",
      icon: "bi-circle-half",
      active: themeMap.value,
      run: () => {
        themeMap.value = !themeMap.value;
      },
    });
  }
  if (previewKind.value === "markdown" || previewKind.value === "html") {
    actions.push({
      id: "toggle-mode",
      title: mode.value === "render" ? "查看源码" : "查看渲染",
      icon: mode.value === "render" ? "bi-code-slash" : "bi-eye",
      active: mode.value === "render",
      run: () => {
        mode.value = mode.value === "render" ? "source" : "render";
      },
    });
  }
  actions.push({
    id: "pin",
    title: pinned.value ? "取消固定（关闭面板后删除此实例）" : "固定实例（关闭面板后保留）",
    icon: pinned.value ? "bi-pin-angle-fill" : "bi-pin-angle",
    active: pinned.value,
    run: () => setPinned(instanceId, !pinned.value),
  });
  ctx.setChrome({ title: label.value, actions });
});
</script>

<template>
  <div class="files-pane">
    <FilePreview :path="file" :mode="mode" :reload-tick="reloadTick" :trim-x="trimX" :trim-y="trimY" :theme-map="themeMap" />
    <div v-if="trimOpen && previewKind === 'pdf'" class="trim-panel">
      <div class="trim-row">
        <span>横向</span>
        <input v-model.number="draftTrimX" type="range" min="0" max="100" step="1" @change="commitTrim" />
        <span class="trim-value">{{ draftTrimX }}%</span>
      </div>
      <div class="trim-row">
        <span>纵向</span>
        <input v-model.number="draftTrimY" type="range" min="0" max="100" step="1" @change="commitTrim" />
        <span class="trim-value">{{ draftTrimY }}%</span>
      </div>
    </div>
    <div v-if="overlayOpen" class="files-overlay">
      <FileBrowser
        :dir="dir"
        @navigate="dir = $event"
        @open="openFile"
        @resolved="dir = $event"
      />
    </div>
  </div>
</template>

<style scoped>
.files-pane {
  background: var(--color-surface);
  color: var(--color-text);
  height: 100%;
  min-width: 0;
  overflow: hidden;
  position: relative;
}

.trim-panel {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  box-shadow: 0 2px 8px rgb(0 0 0 / 12%);
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 10px;
  position: absolute;
  right: 8px;
  top: 4px;
  width: 220px;
  z-index: 6;
}

.trim-row {
  align-items: center;
  color: var(--color-text-subtle);
  display: flex;
  font-size: var(--font-size-ui-small);
  gap: 8px;
}

.trim-row input[type="range"] {
  flex: 1;
  min-width: 0;
}

.trim-value {
  min-width: 34px;
  text-align: right;
}

.files-overlay {
  border-left: 1px solid var(--color-border);
  bottom: 0;
  box-shadow: -4px 0 12px rgb(0 0 0 / 0.08);
  max-width: 70%;
  position: absolute;
  right: 0;
  top: 0;
  width: 300px;
  z-index: 5;
}
</style>
