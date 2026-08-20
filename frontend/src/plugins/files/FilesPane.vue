<script setup lang="ts">
/**
 * Files pane: one virtual instance = one file-preview browser. The overlay
 * file list (FileBrowser, toggled by the floating button) navigates
 * directories and picks files; FilePreview shows the picked file. Per-instance
 * view state and the Dock label persist via instanceStore; the pin action in
 * the pane chrome keeps the instance alive after its pane closes.
 */
import { computed, inject, ref, watch, watchEffect } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { PaneChromeAction } from "../../stores/paneChrome";
import FileBrowser from "./FileBrowser.vue";
import FilePreview from "./FilePreview.vue";
import { getInstance, setPinned, updateState } from "./instanceStore";
import type { PreviewMode } from "./instanceStore";
import { basename, kindForPath } from "./types";
import type { FileEntry } from "./types";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("FilesPane must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;
const instanceId = ctx.instanceId;
// Panes are only opened through the dock provider, which registers the
// instance first; the fallback keeps a stale layout from crashing the pane.
const record = getInstance(instanceId);

const dir = ref(record?.state.dir ?? "");
const file = ref<string | null>(record?.state.file ?? null);
const mode = ref<PreviewMode>(record?.state.mode ?? "render");
const overlayOpen = ref(record?.state.overlayOpen ?? true);
const pinned = computed(() => record?.pinned ?? false);

const previewKind = computed(() => (file.value === null ? null : kindForPath(file.value)));
const label = computed(() => {
  if (file.value !== null) return basename(file.value);
  if (dir.value !== "" && dir.value !== "/") return basename(dir.value);
  return "文件";
});

function openFile(entry: FileEntry): void {
  file.value = entry.path;
}

watch([dir, file, mode, overlayOpen], () => {
  updateState(
    instanceId,
    { dir: dir.value, file: file.value, mode: mode.value, overlayOpen: overlayOpen.value },
    label.value,
  );
});

watchEffect(() => {
  const actions: PaneChromeAction[] = [];
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
    <FilePreview :path="file" :mode="mode" />
    <div v-if="overlayOpen" class="files-overlay">
      <FileBrowser
        :dir="dir"
        @navigate="dir = $event"
        @open="openFile"
        @resolved="dir = $event"
      />
    </div>
    <button
      type="button"
      class="files-fab"
      :title="overlayOpen ? '隐藏文件列表' : '显示文件列表'"
      :aria-label="overlayOpen ? '隐藏文件列表' : '显示文件列表'"
      @click="overlayOpen = !overlayOpen"
    >
      <i class="bi" :class="overlayOpen ? 'bi-x' : 'bi-folder2-open'"></i>
    </button>
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

.files-fab {
  align-items: center;
  background: var(--color-surface-raised);
  border: 1px solid var(--color-border);
  border-radius: 50%;
  bottom: 12px;
  color: var(--color-text-muted);
  display: inline-flex;
  font-size: 13px;
  height: 28px;
  justify-content: center;
  padding: 0;
  position: absolute;
  right: 12px;
  width: 28px;
  z-index: 6;
}

.files-fab:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}
</style>
