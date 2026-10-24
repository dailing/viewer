<script setup lang="ts">
/**
 * Jupyter notebook (.ipynb) preview. The file is fetched through the
 * gateway's by-reference data plane (file:_:ticket → /api/files/raw, no size
 * cap — real notebooks with embedded base64 outputs routinely exceed the bus
 * read cap) and rendered cell by cell in the browser. nbformat parsing lives
 * in notebookModel.ts (pure, unit-testable); this component only renders:
 * markdown cells through the shared markdown pipeline (KaTeX/mermaid
 * included), code cells through highlight.js with the same line-number
 * structure as markdown code fences, plus the normalized output views.
 *
 * Same-path reloads (watch heartbeats, manual refresh) keep the old render
 * mounted while fetching and restore the scroll offset after the swap —
 * the same contract as TexPreview.
 */
import { inject, nextTick, ref, watch } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { renderCodeBlock, renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import type { PreviewMode } from "./instanceStore";
import { parseNotebook } from "./notebookModel";
import type { NotebookOutput } from "./notebookModel";

const props = defineProps<{
  path: string;
  mode: PreviewMode;
  /** Manual refresh + server change events share one tick; a bump refetches. */
  reloadTick: number;
}>();

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("NotebookPreview must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

interface TicketReply {
  url: string;
}

interface CellView {
  type: "markdown" | "code" | "raw";
  /** markdown: rendered html; code: highlighted block html; raw: unused. */
  html: string;
  /** raw: plain source text. */
  text: string;
  execCount: number | null;
  outputs: NotebookOutput[];
}

type Status = "loading" | "ready" | "error";

const status = ref<Status>("loading");
const error = ref("");
const rawText = ref("");
const cells = ref<CellView[]>([]);
const wrapRef = ref<HTMLElement | null>(null);
const rootRef = ref<HTMLElement | null>(null);
let loadedPath: string | null = null;
/** Stale-fetch guard: bumped per load, awaited steps bail when it moved on. */
let fetchToken = 0;

async function load(): Promise<void> {
  const token = ++fetchToken;
  const keepMounted = props.path === loadedPath && status.value === "ready";
  if (!keepMounted) {
    status.value = "loading";
    error.value = "";
  }
  const scrollTop = keepMounted && props.mode === "render" ? (wrapRef.value?.scrollTop ?? null) : null;
  try {
    const ticket = (await ctx.bus.request("file:_:ticket", { path: props.path })) as TicketReply;
    const response = await fetch(ticket.url);
    if (token !== fetchToken) return;
    if (!response.ok) throw new Error(`读取失败（HTTP ${response.status}）`);
    const text = await response.text();
    if (token !== fetchToken) return;
    rawText.value = text;
    const model = parseNotebook(text);
    cells.value = model.cells.map((cell) => ({
      type: cell.type,
      html:
        cell.type === "markdown"
          ? renderMarkdown(cell.source)
          : cell.type === "code"
            ? renderCodeBlock(cell.source, model.language)
            : "",
      text: cell.type === "raw" ? cell.source : "",
      execCount: cell.execCount,
      outputs: cell.outputs,
    }));
    status.value = "ready";
    loadedPath = props.path;
    await nextTick();
    if (token !== fetchToken) return;
    if (scrollTop !== null && wrapRef.value !== null) {
      wrapRef.value.scrollTop = Math.min(scrollTop, wrapRef.value.scrollHeight);
    }
    if (props.mode === "render") void renderMermaidIn(rootRef.value, "files-notebook");
  } catch (cause) {
    if (token !== fetchToken) return;
    // keepMounted reload failure: keep the old render on screen; the next
    // watch heartbeat or manual refresh retries.
    if (!keepMounted) {
      error.value = cause instanceof Error ? cause.message : "读取失败";
      status.value = "error";
    }
  }
}

watch(
  () => [props.path, props.reloadTick],
  () => void load(),
  { immediate: true },
);

// Mode toggles never refetch; the render-side mermaid pass re-runs on remount.
watch(
  () => props.mode,
  (mode) => {
    if (mode === "render" && status.value === "ready") {
      void renderMermaidIn(rootRef.value, "files-notebook");
    }
  },
);
</script>

<template>
  <div ref="wrapRef" class="notebook-wrap">
    <pre v-if="mode === 'source' && rawText !== ''" class="notebook-source">{{ rawText }}</pre>
    <div v-else-if="status === 'loading'" class="preview-notice">
      <i class="bi bi-arrow-repeat"></i>
      <div>正在加载…</div>
    </div>
    <div v-else-if="status === 'error'" class="preview-notice">
      <i class="bi bi-exclamation-triangle"></i>
      <div>{{ error }}</div>
    </div>
    <div v-else ref="rootRef" class="notebook">
      <template v-for="(cell, index) in cells" :key="index">
        <div
          v-if="cell.type === 'markdown'"
          class="nb-cell nb-markdown markdown-content"
          v-html="cell.html"
        ></div>
        <div v-else-if="cell.type === 'raw'" class="nb-cell nb-raw">
          <pre class="nb-text">{{ cell.text }}</pre>
        </div>
        <div v-else class="nb-cell nb-code">
          <div class="nb-prompt" aria-hidden="true">[{{ cell.execCount ?? " " }}]</div>
          <div class="nb-main">
            <div class="nb-source markdown-content" v-html="cell.html"></div>
            <div v-for="(output, outputIndex) in cell.outputs" :key="outputIndex" class="nb-output">
              <template v-if="output.kind === 'text'">
                <pre
                  class="nb-text"
                  :class="{ 'nb-stderr': output.stderr }"
                  v-html="output.html"
                ></pre>
                <div v-if="output.truncated" class="nb-truncated">输出已截断（前 64K 字符）</div>
              </template>
              <img
                v-else-if="output.kind === 'image'"
                class="nb-image"
                :src="output.src"
                :alt="output.alt"
              />
              <iframe
                v-else-if="output.kind === 'html'"
                class="nb-html"
                sandbox=""
                :srcdoc="output.html"
                title="HTML 输出"
              ></iframe>
              <div v-else class="nb-error">
                <div class="nb-error-head">{{ output.ename }}: {{ output.evalue }}</div>
                <pre v-if="output.traceHtml !== ''" class="nb-text nb-stderr" v-html="output.traceHtml"></pre>
              </div>
            </div>
          </div>
        </div>
      </template>
      <div v-if="cells.length === 0" class="preview-notice">
        <i class="bi bi-file-earmark"></i>
        <div>空 notebook</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.notebook-wrap {
  background: var(--color-surface);
  color: var(--color-text);
  height: 100%;
  overflow: auto;
}

.preview-notice {
  align-items: center;
  color: var(--color-text-subtle);
  display: flex;
  flex-direction: column;
  font-size: var(--font-size-ui-small);
  gap: 6px;
  height: 100%;
  justify-content: center;
  padding: 0 24px;
  text-align: center;
}

.preview-notice > i {
  font-size: calc(var(--font-size-ui) * 5 / 3);
}

.notebook-source {
  font-family: var(--font-family-mono, monospace);
  font-size: calc(var(--markdown-body-font-size) * 4 / 5);
  line-height: 1.5;
  margin: 0;
  padding: 12px 16px;
  white-space: pre-wrap;
  word-break: break-all;
}

.notebook {
  font-size: var(--markdown-body-font-size);
  line-height: var(--markdown-body-line-height);
  padding: 8px 0 48px;
}

.nb-markdown {
  padding: 4px 16px;
}

.nb-raw {
  padding: 4px 16px;
}

.nb-code {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  margin: 6px 0;
}

.nb-prompt {
  color: var(--color-text-muted);
  font-family: var(--font-family-mono, monospace);
  font-size: calc(var(--markdown-body-font-size) * 4 / 5);
  min-width: 44px;
  padding: 8px 6px 0 16px;
  text-align: right;
  user-select: none;
}

.nb-main {
  min-width: 0;
  padding-right: 16px;
}

.nb-source :deep(.markdown-code-block) {
  margin: 0;
}

.nb-output {
  margin-top: 4px;
}

.nb-text {
  font-family: var(--font-family-mono, monospace);
  font-size: calc(var(--markdown-body-font-size) * 4 / 5);
  line-height: 1.5;
  margin: 0;
  overflow-x: auto;
  padding: 6px 8px;
  white-space: pre-wrap;
  word-break: break-all;
}

.nb-stderr {
  color: var(--color-danger);
}

.nb-truncated {
  color: var(--color-text-subtle);
  font-size: var(--font-size-ui-small);
  padding: 2px 8px;
}

.nb-image {
  display: block;
  max-width: 100%;
  padding: 4px 8px;
}

.nb-html {
  background: #fff;
  border: 1px solid var(--color-border);
  border-radius: 4px;
  height: 240px;
  margin: 4px 8px;
  width: calc(100% - 16px);
}

.nb-error {
  border-left: 2px solid var(--color-danger);
  margin: 4px 0;
}

.nb-error-head {
  color: var(--color-danger);
  font-family: var(--font-family-mono, monospace);
  font-size: calc(var(--markdown-body-font-size) * 4 / 5);
  font-weight: 600;
  padding: 4px 8px 0;
}
</style>
