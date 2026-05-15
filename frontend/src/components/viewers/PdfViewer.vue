<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import VuePdfEmbed from "vue-pdf-embed";
import "vue-pdf-embed/dist/styles/annotationLayer.css";
import "vue-pdf-embed/dist/styles/textLayer.css";
import { rawUrl } from "../../api/client";
import { restoreScrollPosition, saveScrollPosition } from "../../utils/scrollMemory";

const props = defineProps<{ path: string; contentHash: string; paneId: string; workspaceId: string }>();
const src = computed(() => rawUrl(props.path, props.contentHash));
const container = ref<HTMLElement | null>(null);
const viewerWidth = ref(0);
const zoom = ref(1);
const rotation = ref(0);
const pageCount = ref(0);
const loading = ref(true);
const error = ref("");

let resizeObserver: ResizeObserver | null = null;
let skipUnmountSave = false;

const renderedWidth = computed(() => {
  if (!viewerWidth.value) return undefined;
  return Math.max(240, Math.floor((viewerWidth.value - 32) * zoom.value));
});

function updateWidth(): void {
  if (!container.value) {
    viewerWidth.value = 0;
    return;
  }
  viewerWidth.value = container.value.getBoundingClientRect().width;
}

function zoomOut(): void {
  zoom.value = Math.max(0.5, Number((zoom.value - 0.1).toFixed(2)));
}

function zoomIn(): void {
  zoom.value = Math.min(2.5, Number((zoom.value + 0.1).toFixed(2)));
}

function resetView(): void {
  zoom.value = 1;
  rotation.value = 0;
}

function scrollTarget() {
  return { path: props.path, paneId: props.paneId, workspaceId: props.workspaceId };
}

function saveCurrentScroll(): void {
  saveScrollPosition(scrollTarget(), container.value);
}

function saveBeforePaneNavigate(event: Event): void {
  const targetPaneId = (event as CustomEvent<{ paneId?: string }>).detail?.paneId;
  if (targetPaneId === props.paneId) {
    saveCurrentScroll();
    skipUnmountSave = true;
  }
}

function saveBeforeWorkspaceSwitch(): void {
  saveCurrentScroll();
  skipUnmountSave = true;
}

function rotateClockwise(): void {
  rotation.value = (rotation.value + 90) % 360;
}

function handleLoaded(document: { numPages?: number }): void {
  pageCount.value = document.numPages ?? 0;
}

function handleProgress(): void {
  loading.value = true;
  error.value = "";
}

function handleRendered(): void {
  loading.value = false;
  void restoreScrollPosition(scrollTarget(), container.value);
}

function handlePdfError(err: Error): void {
  loading.value = false;
  error.value = err.message || "Unable to render PDF";
}

watch([src, () => props.workspaceId], () => {
  saveCurrentScroll();
  skipUnmountSave = false;
  loading.value = true;
  error.value = "";
  pageCount.value = 0;
  resetView();
  void nextTick(() => restoreScrollPosition(scrollTarget(), container.value));
});

onMounted(() => {
  updateWidth();
  if (container.value) {
    resizeObserver = new ResizeObserver(updateWidth);
    resizeObserver.observe(container.value);
  }
  window.addEventListener("beforeunload", saveCurrentScroll);
  window.addEventListener("viewer:pane-before-navigate", saveBeforePaneNavigate);
  window.addEventListener("viewer:workspace-before-switch", saveBeforeWorkspaceSwitch);
  void restoreScrollPosition(scrollTarget(), container.value);
});

onUnmounted(() => {
  if (!skipUnmountSave) saveCurrentScroll();
  window.removeEventListener("beforeunload", saveCurrentScroll);
  window.removeEventListener("viewer:pane-before-navigate", saveBeforePaneNavigate);
  window.removeEventListener("viewer:workspace-before-switch", saveBeforeWorkspaceSwitch);
  resizeObserver?.disconnect();
});
</script>

<template>
  <section class="pdf-shell">
    <div class="pdf-toolbar">
      <span class="pdf-status">{{ pageCount ? `${pageCount} pages` : "PDF" }}</span>
      <div class="pdf-actions">
        <button class="pdf-button" type="button" title="Zoom out" aria-label="Zoom out" @click="zoomOut">
          <i class="bi bi-zoom-out"></i>
        </button>
        <span class="pdf-zoom">{{ Math.round(zoom * 100) }}%</span>
        <button class="pdf-button" type="button" title="Zoom in" aria-label="Zoom in" @click="zoomIn">
          <i class="bi bi-zoom-in"></i>
        </button>
        <button class="pdf-button" type="button" title="Rotate" aria-label="Rotate" @click="rotateClockwise">
          <i class="bi bi-arrow-clockwise"></i>
        </button>
        <button class="pdf-button" type="button" title="Reset view" aria-label="Reset view" @click="resetView">
          <i class="bi bi-aspect-ratio"></i>
        </button>
        <a class="pdf-button" :href="src" target="_blank" rel="noreferrer" title="Open raw PDF" aria-label="Open raw PDF">
          <i class="bi bi-box-arrow-up-right"></i>
        </a>
      </div>
    </div>
    <div ref="container" class="pdf-scroll scroll-area" @scroll.passive="saveCurrentScroll">
      <div v-if="loading && !error" class="pdf-loading">
        <div class="spinner-border spinner-border-sm" role="status"></div>
      </div>
      <div v-if="error" class="pdf-error">
        <span>{{ error }}</span>
        <a :href="src" target="_blank" rel="noreferrer">Open PDF</a>
      </div>
      <VuePdfEmbed
        v-else-if="renderedWidth"
        annotation-layer
        text-layer
        class="pdf-document"
        :source="src"
        :width="renderedWidth"
        :rotation="rotation"
        @loaded="handleLoaded"
        @progress="handleProgress"
        @rendered="handleRendered"
        @loading-failed="handlePdfError"
        @rendering-failed="handlePdfError"
      />
    </div>
  </section>
</template>

<style scoped>
.pdf-shell {
  background: #eef2f7;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.pdf-toolbar {
  align-items: center;
  background: #ffffff;
  border-bottom: 1px solid var(--border);
  display: flex;
  flex: 0 0 auto;
  gap: 10px;
  justify-content: space-between;
  min-height: 38px;
  padding: 4px 8px;
}

.pdf-status {
  color: #526070;
  font-size: 12px;
  font-weight: 700;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pdf-actions {
  align-items: center;
  display: flex;
  flex: 0 0 auto;
  gap: 4px;
}

.pdf-button {
  align-items: center;
  background: transparent;
  border: 1px solid transparent;
  border-radius: 6px;
  color: #42526a;
  display: inline-flex;
  height: 28px;
  justify-content: center;
  line-height: 1;
  padding: 0;
  text-decoration: none;
  width: 28px;
}

.pdf-button:hover {
  background: #eef2f7;
  border-color: #d7dee8;
  color: #172033;
}

.pdf-zoom {
  color: #526070;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  min-width: 38px;
  text-align: center;
}

.pdf-scroll {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
  position: relative;
}

.pdf-loading {
  align-items: center;
  color: #526070;
  display: flex;
  gap: 8px;
  justify-content: center;
  padding: 24px;
}

.pdf-error {
  align-items: center;
  color: #a33;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 24px;
  text-align: center;
}

.pdf-document {
  align-items: center;
  display: flex;
  flex-direction: column;
  gap: 14px;
  min-width: max-content;
  padding: 16px;
}

.pdf-document :deep(.vue-pdf-embed__page) {
  background: #ffffff;
  box-shadow: 0 2px 12px rgb(15 23 42 / 0.18);
  margin: 0 auto;
}

.pdf-document :deep(canvas) {
  display: block;
  height: auto;
  max-width: none;
}
</style>
