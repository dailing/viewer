<script setup lang="ts">
import hljs from "highlight.js";
import { computed, nextTick, onUnmounted, ref } from "vue";
import { getText, resolveMarkdownLink } from "../../api/client";
import { useReloadingScrollMemory } from "../../composables/useScrollMemory";
import { useFilesStore } from "../../stores/files";
import { useLayoutStore } from "../../stores/layout";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import { restoreScrollPosition } from "../../utils/scrollMemory";

type MarkdownMode = "rendered" | "raw";

const props = defineProps<{ path: string; version: number; paneId: string }>();
const files = useFilesStore();
const layout = useLayoutStore();
const toolbar = usePaneToolbarStore();
const mode = ref<MarkdownMode>("rendered");
const text = ref("");
const html = ref("");
const error = ref("");
const container = ref<HTMLElement | null>(null);

const highlightedRaw = computed(() => {
  if (!text.value) return "";
  return hljs.highlight(text.value, { language: "markdown", ignoreIllegals: true }).value;
});

function setMode(value: MarkdownMode) {
  mode.value = value;
  registerToolbar();
  void nextTick(async () => {
    if (mode.value === "rendered") await renderMermaidIn(container.value);
    await restoreScrollPosition(props.path, container.value);
  });
}

function registerToolbar() {
  toolbar.setPaneToolbar(props.paneId, {
    title: props.path,
    actions: [
      { id: "markdown-rendered", title: "Rendered Markdown", label: "Rendered", active: mode.value === "rendered", run: () => setMode("rendered") },
      { id: "markdown-raw", title: "Raw Markdown", label: "Raw", active: mode.value === "raw", run: () => setMode("raw") },
    ],
  });
}

async function load() {
  error.value = "";
  try {
    text.value = await getText(props.path);
    html.value = renderMarkdown(text.value, { basePath: props.path });
    registerToolbar();
    await nextTick();
    if (mode.value === "rendered") await renderMermaidIn(container.value);
    await restoreScrollPosition(props.path, container.value);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

const { saveCurrentScroll } = useReloadingScrollMemory(
  () => props.path,
  () => props.version,
  container,
  load,
);

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

onUnmounted(() => {
  toolbar.clearPaneToolbar(props.paneId);
});
</script>

<template>
  <article
    v-if="!error && mode === 'rendered'"
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
</style>
