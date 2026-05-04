<script setup lang="ts">
import { ref } from "vue";
import { getText, resolveMarkdownLink } from "../../api/client";
import { useReloadingScrollMemory } from "../../composables/useScrollMemory";
import { useFilesStore } from "../../stores/files";
import { useLayoutStore } from "../../stores/layout";
import { renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import { restoreScrollPosition } from "../../utils/scrollMemory";

const props = defineProps<{ path: string; version: number }>();
const files = useFilesStore();
const layout = useLayoutStore();
const html = ref("");
const error = ref("");
const container = ref<HTMLElement | null>(null);

async function load() {
  error.value = "";
  try {
    const text = await getText(props.path);
    html.value = renderMarkdown(text, { basePath: props.path });
    await renderMermaidIn(container.value);
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
</script>

<template>
  <article
    v-if="!error"
    ref="container"
    class="markdown-body markdown-content scroll-area"
    @scroll.passive="saveCurrentScroll"
    @click="openMarkdownLink"
    v-html="html"
  ></article>
  <div v-else class="markdown-error">{{ error }}</div>
</template>

<style scoped>
.markdown-body {
  height: 100%;
  padding: 20px;
}

.markdown-error {
  color: #a33;
  padding: 14px;
}
</style>
