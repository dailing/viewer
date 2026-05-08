<script setup lang="ts">
import { defineAsyncComponent, onMounted, onUnmounted, ref, watch } from "vue";
import type { LayoutNode } from "../types/layout";
import type { FileMeta, WatchEvent } from "../types/files";
import { getMeta } from "../api/client";
import { useLayoutStore } from "../stores/layout";
import { fileChangeAffectsPath } from "../utils/paths";
import CodexViewer from "./viewers/CodexViewer.vue";
import ImageViewer from "./viewers/ImageViewer.vue";
import MarkdownViewer from "./viewers/MarkdownViewer.vue";
import TextViewer from "./viewers/TextViewer.vue";
import TerminalViewer from "./viewers/TerminalViewer.vue";
import UnsupportedViewer from "./viewers/UnsupportedViewer.vue";

const props = defineProps<{ pane: Extract<LayoutNode, { type: "pane" }>; workspaceLoading?: boolean }>();
const PdfViewer = defineAsyncComponent(() => import("./viewers/PdfViewer.vue"));
const layout = useLayoutStore();
const meta = ref<FileMeta | null>(null);
const error = ref("");
const version = ref(0);

async function load(clearMeta: boolean) {
  error.value = "";
  if (clearMeta) meta.value = null;
  if (props.workspaceLoading) return;
  if (!props.pane.filePath || props.pane.terminalId || props.pane.codexSessionId) return;
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

watch(() => [props.pane.filePath, props.pane.terminalId, props.pane.codexSessionId, props.workspaceLoading], () => load(true), { immediate: true });
onMounted(() => window.addEventListener("viewer:file-changed", handleChange));
onUnmounted(() => window.removeEventListener("viewer:file-changed", handleChange));
</script>

<template>
  <section class="viewer-pane" :class="{ active: layout.activePaneId === pane.id }" @click="layout.setActive(pane.id)">
    <div class="pane-body">
      <div v-if="workspaceLoading" class="empty-state">
        <div class="spinner-border spinner-border-sm" role="status" aria-label="Loading workspace pane"></div>
      </div>
      <TerminalViewer v-else-if="pane.terminalId" :id="pane.terminalId" :pane-id="pane.id" />
      <CodexViewer v-else-if="pane.codexSessionId" :id="pane.codexSessionId" :pane-id="pane.id" />
      <div v-else-if="!pane.filePath" class="empty-state">
        <i class="bi bi-folder2-open"></i>
        <span>Select a file from the sidebar</span>
      </div>
      <div v-else-if="error" class="empty-state error-state">
        <i class="bi bi-exclamation-triangle"></i>
        <span>{{ error }}</span>
      </div>
      <ImageViewer v-else-if="meta?.preview === 'image'" :path="pane.filePath" :content-hash="meta.content_hash" />
      <MarkdownViewer v-else-if="meta?.preview === 'markdown'" :path="pane.filePath" :version="version" />
      <PdfViewer v-else-if="meta?.preview === 'pdf'" :path="pane.filePath" :content-hash="meta.content_hash" />
      <TextViewer v-else-if="meta?.preview === 'text'" :path="pane.filePath" :version="version" />
      <UnsupportedViewer v-else-if="meta" :meta="meta" />
      <div v-else class="empty-state">
        <div class="spinner-border spinner-border-sm" role="status"></div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.viewer-pane {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 8px;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
}

.viewer-pane.active {
  border-color: #4f6f96;
  box-shadow: 0 0 0 2px rgb(79 111 150 / 0.18);
}

.pane-body {
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
}

.empty-state {
  align-items: center;
  color: var(--text-muted);
  display: flex;
  flex-direction: column;
  gap: 8px;
  height: 100%;
  justify-content: center;
  padding: 18px;
  text-align: center;
}

.empty-state .bi {
  font-size: 28px;
}

.error-state {
  color: #a33;
}
</style>
