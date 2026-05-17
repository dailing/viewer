<script setup lang="ts">
import hljs from "highlight.js";
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { getText, putText, rawUrl, resolveMarkdownLink } from "../../api/client";
import { useReloadingScrollMemory } from "../../composables/useScrollMemory";
import { useFilesStore } from "../../stores/files";
import { useLayoutStore } from "../../stores/layout";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { extractMarkdownImageTargets, isLocalLinkTarget, renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import { fileChangeAffectsPath } from "../../utils/paths";
import { restoreScrollPosition } from "../../utils/scrollMemory";
import type { WatchEvent } from "../../types/files";

type MarkdownMode = "rendered" | "raw";

const props = defineProps<{ path: string; version: number; paneId: string; workspaceId: string }>();
const files = useFilesStore();
const layout = useLayoutStore();
const toolbar = usePaneToolbarStore();
const mode = ref<MarkdownMode>("rendered");
const text = ref("");
const draft = ref("");
const html = ref("");
const error = ref("");
const isEditing = ref(false);
const saving = ref(false);
const container = ref<HTMLElement | null>(null);
const editPreview = ref<HTMLElement | null>(null);
const syncingEditScroll = ref(false);
const imageDependencies = ref<Set<string>>(new Set());
const assetVersion = ref(0);
const imageAssetVersions = ref<Record<string, string>>({});

const editPreviewHtml = computed(() =>
  renderMarkdown(draft.value, { basePath: props.path, assetVersion: assetVersion.value, assetVersions: imageAssetVersions.value }),
);

const highlightedRaw = computed(() => {
  if (!text.value) return "";
  return hljs.highlight(text.value, { language: "markdown", ignoreIllegals: true }).value;
});

function setMode(value: MarkdownMode) {
  mode.value = value;
  registerToolbar();
  void nextTick(async () => {
    if (mode.value === "rendered") await renderMermaidIn(container.value);
    await restoreScrollPosition(scrollTarget(), container.value);
  });
}

function registerToolbar() {
  toolbar.setPaneToolbar(props.paneId, {
    title: props.path,
    actions: [
      { id: "markdown-reload", title: "Reload Markdown", icon: "bi-arrow-clockwise", run: load },
      { id: "markdown-edit", title: isEditing.value ? "Editing Markdown" : "Edit Markdown", icon: "bi-pencil-square", active: isEditing.value, run: toggleEdit },
      { id: "markdown-rendered", title: "Rendered Markdown", label: "Rendered", active: mode.value === "rendered", run: () => setMode("rendered") },
      { id: "markdown-raw", title: "Raw Markdown", label: "Raw", active: mode.value === "raw", run: () => setMode("raw") },
    ],
  });
}

function toggleEdit() {
  if (isEditing.value) {
    cancelEdit();
    return;
  }
  draft.value = text.value;
  isEditing.value = true;
  registerToolbar();
  void renderEditPreview();
}

function cancelEdit() {
  draft.value = text.value;
  isEditing.value = false;
  error.value = "";
  registerToolbar();
}

async function saveEdit() {
  if (saving.value) return;
  saving.value = true;
  error.value = "";
  try {
    await putText(props.path, draft.value);
    assetVersion.value += 1;
    text.value = draft.value;
    await updateImageDependencies(text.value);
    html.value = renderMarkdown(text.value, { basePath: props.path, assetVersion: assetVersion.value, assetVersions: imageAssetVersions.value });
    isEditing.value = false;
    registerToolbar();
    await nextTick();
    rewriteLocalHtmlImages();
    if (mode.value === "rendered") await renderMermaidIn(container.value);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    saving.value = false;
  }
}

async function renderEditPreview() {
  await nextTick();
  await renderMermaidIn(editPreview.value, "edit-mermaid");
  syncScroll(container.value, editPreview.value);
}

function syncScroll(source: HTMLElement | null, target: HTMLElement | null) {
  if (!source || !target || syncingEditScroll.value) return;
  const sourceRange = source.scrollHeight - source.clientHeight;
  const targetRange = target.scrollHeight - target.clientHeight;
  if (sourceRange <= 0 || targetRange <= 0) return;
  syncingEditScroll.value = true;
  target.scrollTop = (source.scrollTop / sourceRange) * targetRange;
  window.requestAnimationFrame(() => {
    syncingEditScroll.value = false;
  });
}

function onEditorScroll() {
  saveCurrentScroll();
  if (isEditing.value) syncScroll(container.value, editPreview.value);
}

function onEditPreviewScroll() {
  syncScroll(editPreview.value, container.value);
}

async function updateImageDependencies(source: string) {
  const targets = extractMarkdownImageTargets(source);
  const resolved = await Promise.allSettled(targets.map((target) => resolveMarkdownLink(props.path, target)));
  const versions: Record<string, string> = {};
  const dependencies: string[] = [];
  resolved.forEach((result, index) => {
    if (result.status !== "fulfilled") return;
    dependencies.push(result.value.path);
    if (result.value.content_hash) versions[targets[index]] = result.value.content_hash;
  });
  imageDependencies.value = new Set(dependencies);
  imageAssetVersions.value = versions;
}

async function load() {
  if (isEditing.value) return;
  error.value = "";
  try {
    assetVersion.value += 1;
    text.value = await getText(props.path);
    await updateImageDependencies(text.value);
    html.value = renderMarkdown(text.value, { basePath: props.path, assetVersion: assetVersion.value, assetVersions: imageAssetVersions.value });
    registerToolbar();
    await nextTick();
    rewriteLocalHtmlImages();
    if (mode.value === "rendered") await renderMermaidIn(container.value);
    await restoreScrollPosition(scrollTarget(), container.value);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function rewriteLocalHtmlImages() {
  for (const image of Array.from(container.value?.querySelectorAll<HTMLImageElement>("img[src]") ?? [])) {
    const src = image.getAttribute("src") ?? "";
    if (src.startsWith("/api/file/raw") || !isLocalLinkTarget(src)) continue;
    image.setAttribute("src", rawUrl(src, String(assetVersion.value), props.path));
  }
}

function handleFileChanged(event: Event) {
  const detail = (event as CustomEvent<WatchEvent>).detail;
  if ([...imageDependencies.value].some((path) => fileChangeAffectsPath(detail.path, path))) {
    void load();
  }
}

const { saveCurrentScroll } = useReloadingScrollMemory(
  () => props.path,
  () => props.version,
  container,
  load,
  () => ({ paneId: props.paneId, workspaceId: props.workspaceId }),
);

function scrollTarget() {
  return { path: props.path, paneId: props.paneId, workspaceId: props.workspaceId };
}

async function openMarkdownLink(event: MouseEvent) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
    return;
  }
  const element = event.target instanceof Element ? event.target : null;
  const link = element?.closest<HTMLAnchorElement>("a[data-viewer-link]");
  const target = link?.dataset.viewerTarget;
  if (!target) {
    return;
  }

  event.preventDefault();
  error.value = "";
  try {
    const resolved = await resolveMarkdownLink(props.path, target);
    await files.recordVisit(resolved.path);
    layout.openFile(resolved.path);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

onMounted(() => {
  window.addEventListener("viewer:file-changed", handleFileChanged);
});

watch(draft, () => {
  if (isEditing.value) void renderEditPreview();
});

onUnmounted(() => {
  window.removeEventListener("viewer:file-changed", handleFileChanged);
  toolbar.clearPaneToolbar(props.paneId);
});
</script>

<template>
  <div v-if="isEditing" class="markdown-editor">
    <div class="markdown-editor-workspace">
      <textarea
        ref="container"
        v-model="draft"
        class="markdown-editor-input"
        spellcheck="false"
        @scroll.passive="onEditorScroll"
      ></textarea>
      <article
        ref="editPreview"
        class="markdown-body markdown-content markdown-editor-preview scroll-area"
        @scroll.passive="onEditPreviewScroll"
        v-html="editPreviewHtml"
      ></article>
    </div>
    <div class="markdown-editor-actions">
      <button class="btn btn-sm btn-primary" type="button" :disabled="saving" @click="saveEdit">
        <i class="bi" :class="saving ? 'bi-arrow-repeat' : 'bi-check2'"></i>
        <span>{{ saving ? "Saving" : "Save" }}</span>
      </button>
      <button class="btn btn-sm btn-outline-secondary" type="button" :disabled="saving" @click="cancelEdit">
        <i class="bi bi-x-lg"></i>
        <span>Cancel</span>
      </button>
    </div>
    <div v-if="error" class="markdown-error">{{ error }}</div>
  </div>
  <article
    v-else-if="!error && mode === 'rendered'"
    ref="container"
    class="markdown-body markdown-content scroll-area"
    @scroll.passive="saveCurrentScroll"
    @click="openMarkdownLink"
    v-html="html"
  ></article>
  <pre v-else-if="!error" ref="container" class="markdown-raw hljs markdown-syntax" @scroll.passive="saveCurrentScroll"><code v-html="highlightedRaw"></code></pre>
  <div v-else class="markdown-error">{{ error }}</div>
</template>

<style scoped>
.markdown-body {
  height: 100%;
  padding: 20px;
}

.markdown-raw {
  background: var(--syntax-background);
  color: var(--syntax-text);
  height: 100%;
  margin: 0;
  overflow: auto;
  padding: 14px;
  user-select: text;
  white-space: pre-wrap;
  word-break: break-word;
}

.markdown-error {
  color: #a33;
  padding: 14px;
}

.markdown-editor {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.markdown-editor-workspace {
  display: grid;
  flex: 1 1 auto;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  min-height: 0;
}

.markdown-editor-input {
  background: var(--syntax-background);
  border: 0;
  color: var(--syntax-text);
  flex: 1 1 auto;
  font-family: var(--bs-font-monospace);
  font-size: 13px;
  line-height: 1.5;
  min-height: 0;
  outline: none;
  padding: 14px;
  resize: none;
  width: 100%;
}

.markdown-editor-preview {
  border-left: 1px solid var(--border);
  min-width: 0;
  overflow: auto;
}

.markdown-editor-actions {
  align-items: center;
  background: var(--panel);
  border-top: 1px solid var(--border);
  display: flex;
  flex: 0 0 auto;
  gap: 8px;
  justify-content: flex-end;
  padding: 8px 10px;
}

.markdown-editor-actions .btn {
  align-items: center;
  display: inline-flex;
  gap: 6px;
}

@media (max-width: 900px) {
  .markdown-editor-workspace {
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: minmax(0, 1fr) minmax(0, 1fr);
  }

  .markdown-editor-preview {
    border-left: 0;
    border-top: 1px solid var(--border);
  }
}
</style>
