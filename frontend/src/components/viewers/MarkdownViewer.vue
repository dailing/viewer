<script setup lang="ts">
import { ref } from "vue";
import { getText } from "../../api/client";
import { useReloadingScrollMemory } from "../../composables/useScrollMemory";
import { renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import { restoreScrollPosition } from "../../utils/scrollMemory";

const props = defineProps<{ path: string; version: number }>();
const html = ref("");
const error = ref("");
const container = ref<HTMLElement | null>(null);

async function load() {
  error.value = "";
  try {
    const text = await getText(props.path);
    html.value = renderMarkdown(text);
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
</script>

<template>
  <article
    v-if="!error"
    ref="container"
    class="markdown-body markdown-content scroll-area"
    @scroll.passive="saveCurrentScroll"
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
