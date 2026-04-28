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
  color: var(--markdown-body-color);
  font-size: var(--markdown-body-font-size);
  height: 100%;
  line-height: var(--markdown-body-line-height);
  padding: 20px;
}

.markdown-body :deep(h1) {
  color: var(--markdown-h1-color);
  font-size: var(--markdown-h1-font-size);
  font-weight: var(--markdown-h1-font-weight);
  line-height: var(--markdown-h1-line-height);
}

.markdown-body :deep(h2) {
  color: var(--markdown-h2-color);
  font-size: var(--markdown-h2-font-size);
  font-weight: var(--markdown-h2-font-weight);
  line-height: var(--markdown-h2-line-height);
}

.markdown-body :deep(h3) {
  color: var(--markdown-h3-color);
  font-size: var(--markdown-h3-font-size);
  font-weight: var(--markdown-h3-font-weight);
  line-height: var(--markdown-h3-line-height);
}

.markdown-body :deep(h4) {
  color: var(--markdown-h4-color);
  font-size: var(--markdown-h4-font-size);
  font-weight: var(--markdown-h4-font-weight);
  line-height: var(--markdown-h4-line-height);
}

.markdown-body :deep(p),
.markdown-body :deep(li) {
  color: var(--markdown-paragraph-color);
  font-size: var(--markdown-paragraph-font-size);
  line-height: var(--markdown-paragraph-line-height);
}

.markdown-body :deep(a) {
  color: var(--markdown-link-color);
}

.markdown-body :deep(img) {
  max-width: 100%;
}

.markdown-body :deep(pre) {
  background: var(--syntax-background);
  border-radius: 6px;
  color: var(--syntax-text);
  overflow: auto;
  padding: 12px;
}

.markdown-body :deep(code) {
  background: var(--markdown-code-background);
  color: var(--markdown-code-color);
  font-size: var(--markdown-code-font-size);
}

.markdown-body :deep(pre code) {
  background: transparent;
}

.markdown-body :deep(.hljs) {
  background: var(--syntax-background);
  color: var(--syntax-text);
}

.markdown-body :deep(.hljs-keyword),
.markdown-body :deep(.hljs-selector-tag),
.markdown-body :deep(.hljs-built_in) {
  color: var(--syntax-keyword);
}

.markdown-body :deep(.hljs-string),
.markdown-body :deep(.hljs-attr) {
  color: var(--syntax-string);
}

.markdown-body :deep(.hljs-number),
.markdown-body :deep(.hljs-literal) {
  color: var(--syntax-number);
}

.markdown-body :deep(.hljs-title),
.markdown-body :deep(.hljs-name) {
  color: var(--syntax-title);
}

.markdown-body :deep(.hljs-comment),
.markdown-body :deep(.hljs-quote) {
  color: var(--syntax-comment);
}

.markdown-body :deep(.hljs-meta),
.markdown-body :deep(.hljs-doctag) {
  color: var(--syntax-meta);
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
  border: 1px solid var(--markdown-border-color);
  padding: 6px 8px;
}

.markdown-error {
  color: #a33;
  padding: 14px;
}
</style>
