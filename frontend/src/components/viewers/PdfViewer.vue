<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, shallowRef, watch } from "vue";
import VuePdfEmbed, { GlobalWorkerOptions, useVuePdfEmbed } from "vue-pdf-embed/dist/index.essential.mjs";
import "vue-pdf-embed/dist/styles/annotationLayer.css";
import "vue-pdf-embed/dist/styles/textLayer.css";
import PdfWorker from "pdfjs-dist/legacy/build/pdf.worker.min.mjs?url";
import { rawUrl } from "../../api/client";
import { restoreScrollPosition, saveScrollPosition } from "../../utils/scrollMemory";

GlobalWorkerOptions.workerSrc = PdfWorker;

const props = defineProps<{ path: string; contentHash: string; version: number; paneId: string; workspaceId: string }>();
const src = computed(() => rawUrl(props.path, `${props.contentHash}-${props.version}`));
const pdfSource = computed(() => ({
  disableAutoFetch: true,
  disableStream: true,
  rangeChunkSize: 256 * 1024,
  url: src.value,
}));
const container = ref<HTMLElement | null>(null);
const viewerWidth = ref(0);
const zoom = ref(1);
const rotation = ref(0);
const pageCount = ref(0);
const loadingDocument = ref(true);
const error = ref("");
const visiblePages = shallowRef(new Set<number>([1]));
const renderedPages = shallowRef(new Set<number>());
const pageElements = new Map<number, Element>();

let resizeObserver: ResizeObserver | null = null;
let pageObserver: IntersectionObserver | null = null;
let skipUnmountSave = false;

const renderedWidth = computed(() => {
  if (!viewerWidth.value) return undefined;
  return Math.max(240, Math.floor((viewerWidth.value - 32) * zoom.value));
});

const estimatedPageHeight = computed(() => {
  if (!renderedWidth.value) return 720;
  return Math.floor(renderedWidth.value * 1.294);
});

const pages = computed(() => Array.from({ length: pageCount.value }, (_, index) => index + 1));

const loading = computed(() => loadingDocument.value && !error.value);

const { doc } = useVuePdfEmbed({
  onError: handlePdfError,
  onProgress: handleLoadProgress,
  source: pdfSource,
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

function handleLoadProgress(): void {
  loadingDocument.value = true;
  error.value = "";
}

function handlePageRendered(page: number): void {
  const next = new Set(renderedPages.value);
  next.add(page);
  renderedPages.value = next;
  loadingDocument.value = false;
  if (page === firstVisiblePage.value) {
    void restoreScrollPosition(scrollTarget(), container.value);
  }
}

function handlePdfError(err: Error): void {
  loadingDocument.value = false;
  error.value = err.message || "Unable to render PDF";
}

function resetRenderedPages(): void {
  renderedPages.value = new Set();
}

function addVisiblePage(page: number): void {
  if (visiblePages.value.has(page)) return;
  const next = new Set(visiblePages.value);
  next.add(page);
  visiblePages.value = next;
}

function removeVisiblePage(page: number): void {
  if (page === 1 || !visiblePages.value.has(page)) return;
  const next = new Set(visiblePages.value);
  next.delete(page);
  visiblePages.value = next;
}

function shouldRenderPage(page: number): boolean {
  if (visiblePages.value.has(page)) return true;
  return visiblePages.value.has(page - 1) || visiblePages.value.has(page + 1);
}

const firstVisiblePage = computed(() => {
  const values = [...visiblePages.value].sort((left, right) => left - right);
  return values[0] ?? 1;
});

function setupPageObserver(): void {
  pageObserver?.disconnect();
  pageObserver = null;
  if (!container.value) return;
  pageObserver = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        const page = Number((entry.target as HTMLElement).dataset.page);
        if (!Number.isFinite(page)) continue;
        if (entry.isIntersecting) addVisiblePage(page);
        else removeVisiblePage(page);
      }
    },
    { root: container.value, rootMargin: "900px 0px" },
  );
  void nextTick(() => {
    for (const element of pageElements.values()) {
      pageObserver?.observe(element);
    }
  });
}

function setPageElement(page: number, element: Element | null): void {
  const previous = pageElements.get(page);
  if (previous) pageObserver?.unobserve(previous);
  if (!element) {
    pageElements.delete(page);
    return;
  }
  pageElements.set(page, element);
  pageObserver?.observe(element);
}

watch(doc, (document) => {
  pageCount.value = document?.numPages ?? 0;
  loadingDocument.value = !document;
  error.value = "";
  visiblePages.value = new Set([1]);
  resetRenderedPages();
  void nextTick(setupPageObserver);
});

watch([renderedWidth, rotation], () => {
  resetRenderedPages();
});

watch([src, () => props.workspaceId], () => {
  saveCurrentScroll();
  skipUnmountSave = false;
  loadingDocument.value = true;
  error.value = "";
  pageCount.value = 0;
  visiblePages.value = new Set([1]);
  resetRenderedPages();
  resetView();
  void nextTick(() => {
    setupPageObserver();
    restoreScrollPosition(scrollTarget(), container.value);
  });
});

watch(pageCount, () => {
  void nextTick(setupPageObserver);
});

function pageFrameStyle(page: number) {
  return {
    minHeight: pageIsRendered(page) ? undefined : `${estimatedPageHeight.value}px`,
    width: renderedWidth.value ? `${renderedWidth.value}px` : "100%",
  };
}

function pageIsRendered(page: number): boolean {
  return renderedPages.value.has(page);
}

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
  setupPageObserver();
});

onUnmounted(() => {
  if (!skipUnmountSave) saveCurrentScroll();
  window.removeEventListener("beforeunload", saveCurrentScroll);
  window.removeEventListener("viewer:pane-before-navigate", saveBeforePaneNavigate);
  window.removeEventListener("viewer:workspace-before-switch", saveBeforeWorkspaceSwitch);
  resizeObserver?.disconnect();
  pageObserver?.disconnect();
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
      <div v-else-if="renderedWidth && pageCount" class="pdf-document">
        <div
          v-for="page in pages"
          :key="page"
          :ref="(element) => setPageElement(page, element as Element | null)"
          class="pdf-page-frame"
          :data-page="page"
          :style="pageFrameStyle(page)"
        >
          <div v-if="!pageIsRendered(page)" class="pdf-page-placeholder">
            <span>Page {{ page }}</span>
          </div>
          <VuePdfEmbed
            v-if="shouldRenderPage(page)"
            annotation-layer
            text-layer
            class="pdf-page"
            :source="pdfSource"
            :page="page"
            :width="renderedWidth"
            :rotation="rotation"
            @rendered="handlePageRendered(page)"
            @loading-failed="handlePdfError"
            @rendering-failed="handlePdfError"
          />
        </div>
      </div>
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

.pdf-page-frame {
  align-items: center;
  background: #ffffff;
  box-shadow: 0 2px 12px rgb(15 23 42 / 0.18);
  display: flex;
  justify-content: center;
  margin: 0 auto;
  position: relative;
}

.pdf-page-placeholder {
  align-items: center;
  color: #8a96a8;
  display: flex;
  font-size: 12px;
  inset: 0;
  justify-content: center;
  position: absolute;
}

.pdf-page {
  position: relative;
  z-index: 1;
}

.pdf-page :deep(.vue-pdf-embed__page) {
  background: #ffffff;
}

.pdf-page :deep(canvas) {
  display: block;
  height: auto;
  max-width: none;
}
</style>
