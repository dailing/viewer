<script setup lang="ts">
import { defineAsyncComponent, onMounted, onUnmounted, ref, watch } from "vue";
import { getMeta } from "../api/client";
import { fileChangeAffectsPath } from "../utils/paths";
import CsvViewer from "./viewers/CsvViewer.vue";
import HtmlViewer from "./viewers/HtmlViewer.vue";
import ImageViewer from "./viewers/ImageViewer.vue";
import MarkdownViewer from "./viewers/MarkdownViewer.vue";
import TextViewer from "./viewers/TextViewer.vue";
import UnsupportedViewer from "./viewers/UnsupportedViewer.vue";
import type { FileMeta, WatchEvent } from "../types/files";
import type { SplitDirection } from "../types/layout";

const props = defineProps<{ path: string }>();
const emit = defineEmits<{
  close: [];
  openPane: [path: string];
  openSplit: [path: string, direction: SplitDirection];
}>();

const PdfViewer = defineAsyncComponent(() => import("./viewers/PdfViewer.vue"));
const meta = ref<FileMeta | null>(null);
const error = ref("");
const version = ref(0);
const previewPaneId = `preview-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

async function load(clearMeta: boolean) {
  error.value = "";
  if (clearMeta) meta.value = null;
  try {
    meta.value = await getMeta(props.path);
    version.value += 1;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function isCsvPath(path: string): boolean {
  return path.toLowerCase().endsWith(".csv");
}

function handleChange(event: Event) {
  const detail = (event as CustomEvent<WatchEvent>).detail;
  if (fileChangeAffectsPath(detail.path, props.path)) void load(false);
}

function handleKeydown(event: KeyboardEvent) {
  if (event.key !== "Escape") return;
  event.preventDefault();
  emit("close");
}

function openPane() {
  emit("openPane", props.path);
}

function openSplit(direction: SplitDirection) {
  emit("openSplit", props.path, direction);
}

watch(() => props.path, () => load(true), { immediate: true });
onMounted(() => {
  window.addEventListener("viewer:file-changed", handleChange);
  window.addEventListener("keydown", handleKeydown);
});
onUnmounted(() => {
  window.removeEventListener("viewer:file-changed", handleChange);
  window.removeEventListener("keydown", handleKeydown);
});
</script>

<template>
  <div class="local-preview-backdrop" @mousedown.self="emit('close')">
    <section class="local-preview-panel" role="dialog" aria-modal="true" :aria-label="`Preview ${path}`">
      <header class="local-preview-header">
        <div class="local-preview-title" :title="path">{{ path }}</div>
        <div class="local-preview-actions">
          <button class="btn btn-outline-primary btn-sm" type="button" title="Open in pane" @click="openPane">
            <i class="bi bi-box-arrow-in-down-right"></i>
            <span>Open in pane</span>
          </button>
          <button class="btn btn-outline-secondary btn-sm" type="button" title="Open V split" @click="openSplit('vertical')">
            <i class="bi bi-layout-split"></i>
            <span>Open V split</span>
          </button>
          <button class="btn btn-outline-secondary btn-sm" type="button" title="Open H split" @click="openSplit('horizontal')">
            <i class="bi bi-view-stacked"></i>
            <span>Open H split</span>
          </button>
          <button class="btn btn-outline-secondary icon-button local-preview-close" type="button" title="Close preview" @click="emit('close')">
            <i class="bi bi-x-lg"></i>
          </button>
        </div>
      </header>
      <div class="local-preview-body">
        <div v-if="error" class="local-preview-state local-preview-error">
          <i class="bi bi-exclamation-triangle"></i>
          <span>{{ error }}</span>
        </div>
        <ImageViewer v-else-if="meta?.preview === 'image'" :path="path" :content-hash="meta.content_hash" />
        <MarkdownViewer v-else-if="meta?.preview === 'markdown'" :path="path" :version="version" :pane-id="previewPaneId" />
        <HtmlViewer v-else-if="meta?.preview === 'html'" :path="path" :version="version" :pane-id="previewPaneId" :content-hash="meta.content_hash" />
        <PdfViewer v-else-if="meta?.preview === 'pdf'" :path="path" :content-hash="meta.content_hash" />
        <CsvViewer v-else-if="meta?.preview === 'text' && isCsvPath(path)" :path="path" :version="version" :pane-id="previewPaneId" />
        <TextViewer v-else-if="meta?.preview === 'text'" :path="path" :version="version" />
        <UnsupportedViewer v-else-if="meta" :meta="meta" />
        <div v-else class="local-preview-state">
          <div class="spinner-border spinner-border-sm" role="status"></div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.local-preview-backdrop {
  background: rgb(15 23 42 / 0.32);
  inset: var(--topbar-height) 0 0 0;
  padding: 28px;
  position: fixed;
  z-index: 60;
}

.local-preview-panel {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 20px 60px rgb(15 23 42 / 0.26);
  display: flex;
  flex-direction: column;
  height: min(86vh, 900px);
  margin: 0 auto;
  max-width: 1180px;
  min-height: 0;
  overflow: hidden;
  width: min(92vw, 1180px);
}

.local-preview-header {
  align-items: center;
  border-bottom: 1px solid var(--border);
  display: flex;
  flex: 0 0 auto;
  gap: 10px;
  min-height: 40px;
  padding: 5px 8px 5px 12px;
}

.local-preview-title {
  color: #253044;
  flex: 1 1 auto;
  font-size: 12px;
  font-weight: 700;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.local-preview-actions {
  align-items: center;
  display: flex;
  flex: 0 0 auto;
  gap: 5px;
}

.local-preview-actions .btn-sm {
  align-items: center;
  display: inline-flex;
  font-size: 11px;
  gap: 5px;
  line-height: 1;
  min-height: 28px;
}

.local-preview-close {
  --nav-button-size: 28px;
  --nav-icon-size: 12px;
}

.local-preview-body {
  flex: 1 1 auto;
  min-height: 0;
  overflow: hidden;
}

.local-preview-state {
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

.local-preview-error {
  color: #a33;
}

@media (max-width: 767.98px) {
  .local-preview-backdrop {
    padding: 8px;
  }

  .local-preview-panel {
    height: calc(100vh - var(--topbar-height) - 16px);
    width: calc(100vw - 16px);
  }

  .local-preview-header {
    align-items: stretch;
    flex-direction: column;
  }

  .local-preview-actions {
    overflow-x: auto;
  }

  .local-preview-actions .btn-sm {
    white-space: nowrap;
  }
}
</style>
