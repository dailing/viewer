<script setup lang="ts">
import { defineAsyncComponent, onMounted, onUnmounted, ref, watch } from "vue";
import type { LayoutNode } from "../types/layout";
import type { FileMeta, WatchEvent } from "../types/files";
import { getMeta } from "../api/client";
import { useLayoutStore } from "../stores/layout";
import { fileChangeAffectsPath } from "../utils/paths";
import CsvViewer from "./viewers/CsvViewer.vue";
import DiffViewer from "./viewers/DiffViewer.vue";
import HtmlViewer from "./viewers/HtmlViewer.vue";
import ImageViewer from "./viewers/ImageViewer.vue";
import LargeTextViewer from "./viewers/LargeTextViewer.vue";
import MarkdownViewer from "./viewers/MarkdownViewer.vue";
import TextViewer from "./viewers/TextViewer.vue";
import TerminalViewer from "./viewers/TerminalViewer.vue";
import UnsupportedViewer from "./viewers/UnsupportedViewer.vue";
import SuperWorkspaceChatPane from "./SuperWorkspaceChatPane.vue";
import PaneTitleBar from "./PaneTitleBar.vue";

const props = defineProps<{ pane: Extract<LayoutNode, { type: "pane" }>; workspaceId: string; workspaceLoading?: boolean }>();
const PdfViewer = defineAsyncComponent(() => import("./viewers/PdfViewer.vue"));
const layout = useLayoutStore();
const meta = ref<FileMeta | null>(null);
const error = ref("");
const version = ref(0);

async function load(clearMeta: boolean) {
  error.value = "";
  if (clearMeta) meta.value = null;
  if (props.workspaceLoading) return;
  if (!props.pane.filePath || props.pane.terminalId) return;
  try {
    meta.value = await getMeta(props.pane.filePath);
    version.value += 1;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function handleChange(event: Event) {
  const detail = (event as CustomEvent<WatchEvent>).detail;
  if (props.pane.filePath && fileChangeAffectsPath(detail.path, props.pane.filePath)) void load(false);
}

function handleRefresh(event: Event) {
  const paneId = (event as CustomEvent<{ paneId?: string }>).detail?.paneId;
  if (paneId === props.pane.id) void load(true);
}

function isCsvPath(path: string): boolean {
  return path.toLowerCase().endsWith(".csv");
}

watch(() => [props.pane.filePath, props.pane.terminalId, props.workspaceLoading], () => load(true), { immediate: true });
onMounted(() => {
  window.addEventListener("viewer:file-changed", handleChange);
  window.addEventListener("viewer:pane-refresh", handleRefresh);
});
onUnmounted(() => {
  window.removeEventListener("viewer:file-changed", handleChange);
  window.removeEventListener("viewer:pane-refresh", handleRefresh);
});
</script>

<template>
  <section class="viewer-pane" :class="{ active: layout.activePaneId === pane.id }" @click="layout.setActive(pane.id)">
    <PaneTitleBar :pane="pane" />
    <div class="pane-body">
      <div v-if="workspaceLoading" class="empty-state">
        <div class="spinner-border spinner-border-sm" role="status" aria-label="Loading workspace pane"></div>
      </div>
      <TerminalViewer v-else-if="pane.terminalId" :id="pane.terminalId" :pane-id="pane.id" />
      <SuperWorkspaceChatPane v-else-if="pane.chatId" :chat-id="pane.chatId" :pane-id="pane.id" />
      <DiffViewer v-else-if="pane.diffPath" :path="pane.diffPath" :cwd="pane.diffCwd ?? ''" :pane-id="pane.id" />
      <div v-else-if="!pane.filePath" class="empty-state">
        <i class="bi bi-folder2-open"></i>
        <span>Select a file from the sidebar</span>
      </div>
      <div v-else-if="error" class="empty-state error-state">
        <i class="bi bi-exclamation-triangle"></i>
        <span>{{ error }}</span>
      </div>
      <ImageViewer v-else-if="meta?.preview === 'image'" :path="pane.filePath" :content-hash="meta.content_hash" :version="version" />
      <LargeTextViewer v-else-if="meta?.preview === 'markdown' && meta.text_too_large" :path="pane.filePath" :version="version" :pane-id="pane.id" :workspace-id="workspaceId" kind="markdown" :size="meta.size" />
      <MarkdownViewer v-else-if="meta?.preview === 'markdown'" :path="pane.filePath" :version="version" :pane-id="pane.id" :workspace-id="workspaceId" />
      <HtmlViewer v-else-if="meta?.preview === 'html'" :path="pane.filePath" :version="version" :pane-id="pane.id" :workspace-id="workspaceId" :content-hash="meta.content_hash" />
      <PdfViewer v-else-if="meta?.preview === 'pdf'" :path="pane.filePath" :content-hash="meta.content_hash" :version="version" :pane-id="pane.id" :workspace-id="workspaceId" />
      <LargeTextViewer v-else-if="meta?.preview === 'text' && meta.text_too_large && isCsvPath(pane.filePath)" :path="pane.filePath" :version="version" :pane-id="pane.id" :workspace-id="workspaceId" kind="csv" :size="meta.size" />
      <CsvViewer v-else-if="meta?.preview === 'text' && isCsvPath(pane.filePath)" :path="pane.filePath" :version="version" :pane-id="pane.id" :workspace-id="workspaceId" />
      <LargeTextViewer v-else-if="meta?.preview === 'text' && meta.text_too_large" :path="pane.filePath" :version="version" :pane-id="pane.id" :workspace-id="workspaceId" kind="text" :size="meta.size" />
      <TextViewer v-else-if="meta?.preview === 'text'" :path="pane.filePath" :version="version" :pane-id="pane.id" :workspace-id="workspaceId" />
      <UnsupportedViewer v-else-if="meta" :meta="meta" />
      <div v-else class="empty-state">
        <div class="spinner-border spinner-border-sm" role="status"></div>
      </div>
      <button
        v-if="meta?.preview === 'html' && layout.activePaneId !== pane.id"
        class="pane-activation-shield"
        type="button"
        title="Activate pane"
        aria-label="Activate HTML preview pane"
        @pointerdown.stop.prevent="layout.setActive(pane.id)"
        @click.stop.prevent
      ></button>
    </div>
  </section>
</template>

<style scoped>
.viewer-pane {
  background: var(--color-surface);
  border: 0;
  border-radius: 0;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
}

.pane-body {
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
  position: relative;
}

.pane-activation-shield {
  background: transparent;
  border: 0;
  cursor: default;
  inset: 0;
  padding: 0;
  position: absolute;
  z-index: 2;
}

.empty-state {
  align-items: center;
  color: var(--color-text-muted);
  display: flex;
  flex-direction: column;
  gap: 6px;
  height: 100%;
  justify-content: center;
  padding: 10px;
  text-align: center;
}

.empty-state .bi {
  font-size: 28px;
}

.error-state {
  color: var(--color-danger);
}
</style>
