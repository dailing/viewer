<script setup lang="ts">
import MarkdownIt from "markdown-it";
import anchor from "markdown-it-anchor";
import attrs from "markdown-it-attrs";
import deflist from "markdown-it-deflist";
import footnote from "markdown-it-footnote";
import mark from "markdown-it-mark";
import sub from "markdown-it-sub";
import sup from "markdown-it-sup";
import taskLists from "markdown-it-task-lists";
import texmath from "markdown-it-texmath";
import hljs from "highlight.js";
import katex from "katex";
import mermaid from "mermaid";
import { nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { getText } from "../../api/client";
import { restoreScrollPosition, saveScrollPosition } from "../../utils/scrollMemory";

const props = defineProps<{ path: string; version: number }>();
const html = ref("");
const error = ref("");
const container = ref<HTMLElement | null>(null);

function persistCurrentScroll() {
  saveScrollPosition(props.path, container.value);
}

mermaid.initialize({ startOnLoad: false, securityLevel: "loose" });

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

const md: MarkdownIt = new MarkdownIt({
  html: true,
  linkify: true,
  typographer: true,
  highlight(code: string, language: string): string {
    if (language && hljs.getLanguage(language)) {
      return `<pre class="hljs"><code>${hljs.highlight(code, { language }).value}</code></pre>`;
    }
    return `<pre class="hljs"><code>${escapeHtml(code)}</code></pre>`;
  },
})
  .use(sub)
  .use(sup)
  .use(mark)
  .use(footnote)
  .use(deflist)
  .use(taskLists, { enabled: false })
  .use(anchor)
  .use(attrs)
  .use(texmath, {
    engine: katex,
    delimiters: "dollars",
    katexOptions: { throwOnError: false },
  });

const fence = md.renderer.rules.fence;
md.renderer.rules.fence = (tokens: any[], idx: number, options: any, env: any, self: any): string => {
  const token = tokens[idx];
  if (token.info.trim().split(/\s+/)[0] === "mermaid") {
    return `<pre class="mermaid">${md.utils.escapeHtml(token.content)}</pre>`;
  }
  return fence ? fence(tokens, idx, options, env, self) : self.renderToken(tokens, idx, options);
};

async function renderMermaid() {
  await nextTick();
  if (!container.value) return;
  const nodes = Array.from(container.value.querySelectorAll<HTMLElement>(".mermaid"));
  await Promise.all(
    nodes.map(async (node, index) => {
      const source = node.textContent ?? "";
      try {
        const result = await mermaid.render(`mermaid-${Date.now()}-${index}`, source);
        node.outerHTML = result.svg;
      } catch {
        node.classList.add("mermaid-error");
      }
    }),
  );
}

async function load() {
  error.value = "";
  try {
    const text = await getText(props.path);
    html.value = md.render(text);
    await renderMermaid();
    await restoreScrollPosition(props.path, container.value);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

onMounted(() => {
  window.addEventListener("beforeunload", persistCurrentScroll);
  void load();
});
onUnmounted(() => {
  persistCurrentScroll();
  window.removeEventListener("beforeunload", persistCurrentScroll);
});
watch(
  () => [props.path, props.version] as const,
  ([newPath], [oldPath, oldVersion]) => {
    if (oldPath && newPath !== oldPath) {
      saveScrollPosition(oldPath, container.value);
    } else if (oldVersion !== undefined) {
      saveScrollPosition(newPath, container.value);
    }
    void load();
  },
);
</script>

<template>
  <article
    v-if="!error"
    ref="container"
    class="markdown-body scroll-area"
    @scroll.passive="saveScrollPosition(path, container)"
    v-html="html"
  ></article>
  <div v-else class="markdown-error">{{ error }}</div>
</template>

<style scoped>
.markdown-body {
  height: 100%;
  padding: 20px;
}

.markdown-body :deep(img) {
  max-width: 100%;
}

.markdown-body :deep(pre) {
  border-radius: 6px;
  overflow: auto;
  padding: 12px;
}

.markdown-body :deep(table) {
  border-collapse: collapse;
  display: block;
  overflow: auto;
  width: max-content;
  max-width: 100%;
}

.markdown-body :deep(th),
.markdown-body :deep(td) {
  border: 1px solid var(--border);
  padding: 6px 8px;
}

.markdown-error {
  color: #a33;
  padding: 14px;
}
</style>
