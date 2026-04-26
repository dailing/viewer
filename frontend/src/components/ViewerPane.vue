<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from "vue";
import type { LayoutNode } from "../types/layout";
import type { FileMeta, WatchEvent } from "../types/files";
import { getMeta } from "../api/client";
import { useLayoutStore } from "../stores/layout";
import ImageViewer from "./viewers/ImageViewer.vue";
import MarkdownViewer from "./viewers/MarkdownViewer.vue";
import PdfViewer from "./viewers/PdfViewer.vue";
import TextViewer from "./viewers/TextViewer.vue";
import TerminalViewer from "./viewers/TerminalViewer.vue";
import UnsupportedViewer from "./viewers/UnsupportedViewer.vue";

const props = defineProps<{ pane: Extract<LayoutNode, { type: "pane" }> }>();
const layout = useLayoutStore();
const meta = ref<FileMeta | null>(null);
const error = ref("");
const version = ref(0);

async function load(clearMeta: boolean) {
  error.value = "";
  if (clearMeta) meta.value = null;
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
  if (detail.path === props.pane.filePath) void load(false);
}

watch(() => props.pane.filePath, () => load(true), { immediate: true });
watch(() => props.pane.terminalId, () => load(true));
onMounted(() => window.addEventListener("viewer:file-changed", handleChange));
onUnmounted(() => window.removeEventListener("viewer:file-changed", handleChange));
</script>

<template>
  <section class="viewer-pane" :class="{ active: layout.activePaneId === pane.id }" @click="layout.setActive(pane.id)">
    <header class="pane-toolbar">
      <div class="pane-title" :title="pane.filePath || pane.terminalId">
        {{ pane.terminalId ? "Terminal" : pane.filePath || "Empty pane" }}
      </div>
      <button class="btn btn-sm btn-outline-secondary icon-button" type="button" title="Split vertical" @click.stop="layout.splitPane(pane.id, 'vertical')">
        <i class="bi bi-layout-split"></i>
      </button>
      <button class="btn btn-sm btn-outline-secondary icon-button" type="button" title="Split horizontal" @click.stop="layout.splitPane(pane.id, 'horizontal')">
        <i class="bi bi-distribute-vertical"></i>
      </button>
      <button class="btn btn-sm btn-outline-secondary icon-button" type="button" title="Close pane" @click.stop="layout.closePane(pane.id)">
        <i class="bi bi-x"></i>
      </button>
    </header>

    <div class="pane-body">
      <TerminalViewer v-if="pane.terminalId" :id="pane.terminalId" />
      <div v-else-if="!pane.filePath" class="empty-state">
        <i class="bi bi-folder2-open"></i>
        <span>Select a file from the sidebar</span>
      </div>
      <div v-else-if="error" class="empty-state error-state">
        <i class="bi bi-exclamation-triangle"></i>
        <span>{{ error }}</span>
      </div>
      <ImageViewer v-else-if="meta?.preview === 'image'" :path="pane.filePath" :version="version" />
      <MarkdownViewer v-else-if="meta?.preview === 'markdown'" :path="pane.filePath" :version="version" />
      <PdfViewer v-else-if="meta?.preview === 'pdf'" :path="pane.filePath" :version="version" />
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

.pane-toolbar {
  align-items: center;
  border-bottom: 1px solid var(--border);
  display: flex;
  flex: 0 0 42px;
  gap: 6px;
  padding: 4px 6px 4px 10px;
}

.pane-title {
  flex: 1 1 auto;
  font-size: 13px;
  font-weight: 600;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
