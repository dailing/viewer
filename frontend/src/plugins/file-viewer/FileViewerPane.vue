<script setup lang="ts">
/** Viewer-only pane. File selection belongs to File Manager; this instance
 * owns the opened path, render mode and PDF presentation settings. */
import { computed, inject, ref, watch, watchEffect } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { PaneChromeAction } from "../../stores/paneChrome";
import { fileTicketUrl } from "../../services/fileData";
import { basename, kindForPath } from "../../services/fileTypes";
import FilePreview from "./FilePreview.vue";
import { getInstance, setPinned, updateState } from "./instanceStore";
import type { FileViewerState, PreviewMode } from "./instanceStore";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("FileViewerPane must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;
const instanceId = ctx.instanceId;
const record = computed(() => getInstance(instanceId));
const file = ref<string | null>(null);
const mode = ref<PreviewMode>("render");
const trimX = ref(100);
const trimY = ref(100);
const themeMap = ref(true);
const draftTrimX = ref(100);
const draftTrimY = ref(100);
const trimOpen = ref(false);
const reloadTick = ref(0);
const pinned = computed(() => record.value?.pinned ?? false);

function adoptState(state: FileViewerState): void {
  file.value = state.path;
  mode.value = state.mode;
  trimX.value = state.trimX;
  trimY.value = state.trimY;
  themeMap.value = state.themeMap;
}
watch(() => record.value?.state, (state) => { if (state !== undefined) adoptState(state); }, { immediate: true });
watch([file, mode, trimX, trimY, themeMap], () => {
  updateState(instanceId, {
    path: file.value,
    mode: mode.value,
    trimX: trimX.value,
    trimY: trimY.value,
    themeMap: themeMap.value,
  }, label.value);
});

const previewKind = computed(() => file.value === null ? null : kindForPath(file.value));
const label = computed(() => file.value === null ? "文件预览" : basename(file.value));
function commitTrim(): void {
  trimX.value = draftTrimX.value;
  trimY.value = draftTrimY.value;
}
async function download(): Promise<void> {
  const path = file.value;
  if (path === null) return;
  const url = await fileTicketUrl(ctx.bus, path);
  const anchor = document.createElement("a");
  anchor.href = `${url}&download=1`;
  anchor.download = basename(path);
  anchor.click();
}

watchEffect(() => {
  const actions: PaneChromeAction[] = [];
  if (file.value !== null) {
    actions.push({
      id: "new-view",
      title: "在新预览面板打开此文件",
      icon: "bi-columns-gap",
      active: false,
      run: () => { void ctx.shell.open({ paneType: "file-viewer", params: { path: file.value }, newPane: true }); },
    });
    actions.push({
      id: "refresh",
      title: "刷新（重新读取文件）",
      icon: "bi-arrow-clockwise",
      active: false,
      run: () => { reloadTick.value += 1; },
    });
    actions.push({ id: "download", title: "下载源文件", icon: "bi-download", active: false, run: download });
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
      run: () => { themeMap.value = !themeMap.value; },
    });
  }
  if (["markdown", "html", "latex", "notebook"].includes(previewKind.value ?? "")) {
    actions.push({
      id: "toggle-mode",
      title: mode.value === "render" ? "查看源码" : "查看渲染",
      icon: mode.value === "render" ? "bi-code-slash" : "bi-eye",
      active: mode.value === "render",
      run: () => { mode.value = mode.value === "render" ? "source" : "render"; },
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
  <div class="file-viewer-pane">
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
  </div>
</template>

<style scoped>
.file-viewer-pane {
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
.trim-row input[type="range"] { flex: 1; min-width: 0; }
.trim-value { min-width: 34px; text-align: right; }
</style>
