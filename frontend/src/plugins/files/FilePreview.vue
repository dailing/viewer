<script setup lang="ts">
/**
 * File preview area: routes by extension — image (data URL), markdown / html
 * (rendered, with a source mode toggled from the pane chrome), otherwise
 * utf-8 text. Oversized reads come back as `too_large` and binary files as
 * base64; both get a plain notice instead of a preview.
 */
import { computed, inject, ref, watch } from "vue";

import { RpcError } from "@viewer/bus-sdk";

import type { PluginCtx } from "../../shell/ctx";
import { renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import type { PreviewMode } from "./instanceStore";
import { imageMimeFor, kindForPath } from "./types";

const props = defineProps<{
  path: string | null;
  mode: PreviewMode;
}>();

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("FilePreview must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const IMAGE_MAX_BYTES = 16 * 1024 * 1024;
const TEXT_MAX_BYTES = 4 * 1024 * 1024;

type Status = "empty" | "loading" | "ready" | "too-large" | "binary" | "error";

interface ReadResult {
  path: string;
  size: number;
  encoding: "utf-8" | "base64";
  content: string;
}

const status = ref<Status>("empty");
const error = ref("");
const limit = ref(0);
const text = ref("");
const imageUrl = ref("");
const renderedRef = ref<HTMLElement | null>(null);

const kind = computed(() => (props.path === null ? null : kindForPath(props.path)));
const rendered = computed(() =>
  kind.value === "markdown" && props.mode === "render" && status.value === "ready"
    ? renderMarkdown(text.value)
    : "",
);

function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${bytes} B`;
}

async function load(path: string | null): Promise<void> {
  status.value = path === null ? "empty" : "loading";
  error.value = "";
  text.value = "";
  imageUrl.value = "";
  if (path === null) return;
  const previewKind = kindForPath(path);
  const maxBytes = previewKind === "image" ? IMAGE_MAX_BYTES : TEXT_MAX_BYTES;
  try {
    const result = (await ctx.bus.request("file:_:read", {
      path,
      max_bytes: maxBytes,
    })) as ReadResult;
    if (previewKind === "image") {
      const mime = imageMimeFor(path);
      imageUrl.value =
        result.encoding === "base64"
          ? `data:${mime};base64,${result.content}`
          : `data:${mime};charset=utf-8,${encodeURIComponent(result.content)}`;
    } else {
      if (result.encoding === "base64") {
        status.value = "binary";
        return;
      }
      text.value = result.content;
    }
    status.value = "ready";
  } catch (cause) {
    if (cause instanceof RpcError && cause.code === "too_large") {
      limit.value = maxBytes;
      status.value = "too-large";
    } else {
      error.value = cause instanceof Error ? cause.message : "读取失败";
      status.value = "error";
    }
  }
}

watch(() => props.path, (path) => void load(path), { immediate: true });

// Mermaid fences need a post-render pass once the v-html is in the DOM.
watch([rendered, renderedRef], () => {
  if (rendered.value !== "") void renderMermaidIn(renderedRef.value, "files-preview");
});
</script>

<template>
  <div class="file-preview">
    <div v-if="status === 'empty'" class="preview-notice">
      <i class="bi bi-file-earmark"></i>
      <div>从文件列表选择文件</div>
    </div>
    <div v-else-if="status === 'loading'" class="preview-notice">
      <i class="bi bi-arrow-repeat"></i>
      <div>正在加载…</div>
    </div>
    <div v-else-if="status === 'too-large'" class="preview-notice">
      <i class="bi bi-exclamation-circle"></i>
      <div>文件太大，无法预览（上限 {{ formatBytes(limit) }}）</div>
    </div>
    <div v-else-if="status === 'binary'" class="preview-notice">
      <i class="bi bi-file-earmark-binary"></i>
      <div>二进制文件，无法预览</div>
    </div>
    <div v-else-if="status === 'error'" class="preview-notice">
      <i class="bi bi-exclamation-triangle"></i>
      <div>{{ error }}</div>
    </div>
    <template v-else>
      <div v-if="kind === 'image'" class="preview-image">
        <img :src="imageUrl" :alt="path ?? ''" />
      </div>
      <iframe
        v-else-if="kind === 'html' && mode === 'render'"
        class="preview-html"
        sandbox=""
        :srcdoc="text"
        title="HTML 预览"
      ></iframe>
      <div
        v-else-if="kind === 'markdown' && mode === 'render'"
        ref="renderedRef"
        class="preview-markdown markdown-content"
        v-html="rendered"
      ></div>
      <pre v-else class="preview-text">{{ text }}</pre>
    </template>
  </div>
</template>

<style scoped>
.file-preview {
  background: var(--color-surface);
  color: var(--color-text);
  height: 100%;
  min-width: 0;
  overflow: hidden;
  position: relative;
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
  font-size: 20px;
}

.preview-image {
  align-items: center;
  display: flex;
  height: 100%;
  justify-content: center;
  overflow: auto;
  padding: 12px;
}

.preview-image img {
  max-height: 100%;
  max-width: 100%;
  object-fit: contain;
}

.preview-html {
  background: #fff;
  border: 0;
  height: 100%;
  width: 100%;
}

.preview-markdown {
  height: 100%;
  overflow: auto;
  padding: 12px 16px;
}

.preview-text {
  font-family: var(--font-family-mono, monospace);
  font-size: 12px;
  height: 100%;
  line-height: 1.5;
  margin: 0;
  overflow: auto;
  padding: 12px 16px;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
