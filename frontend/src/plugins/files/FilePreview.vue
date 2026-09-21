<script setup lang="ts">
/**
 * File preview area: routes by extension — image (data URL), markdown / html
 * (rendered, with a source mode toggled from the pane chrome), otherwise
 * utf-8 text. Oversized reads come back as `too_large` and binary files as
 * base64; both get a plain notice instead of a preview.
 *
 * Open files are watched server-side (file:_:watch, renewed on an interval;
 * the server entry expires if the browser dies): an external change pushes
 * one `file:_:changed` event and the preview reloads. The watch RPC reply
 * doubles as a digest heartbeat — after a background-tab throttle or bus
 * reconnect, a baseline mismatch triggers the same reload. The pane chrome's
 * refresh action bumps `reloadTick` for a manual re-read.
 */
import { computed, inject, nextTick, onUnmounted, ref, watch } from "vue";

import { RpcError } from "@viewer/bus-sdk";
import type { BusFrame } from "@viewer/bus-sdk";

import type { PluginCtx } from "../../shell/ctx";
import { renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import type { PreviewMode } from "./instanceStore";
import PdfPreview from "./PdfPreview.vue";
import { imageMimeFor, kindForPath } from "./types";

const props = defineProps<{
  path: string | null;
  mode: PreviewMode;
  /** Bumped by the pane's manual-refresh action; forces a re-read. */
  reloadTick: number;
  /** PDF margin crop percentages (0–100 per axis); a change remounts PdfPreview. */
  trimX: number;
  trimY: number;
}>();

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("FilePreview must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const IMAGE_MAX_BYTES = 16 * 1024 * 1024;
const TEXT_MAX_BYTES = 4 * 1024 * 1024;
const WATCH_RENEW_MS = 60_000;

type Status = "empty" | "loading" | "ready" | "too-large" | "binary" | "deleted" | "error";

interface ReadResult {
  path: string;
  size: number;
  encoding: "utf-8" | "base64";
  content: string;
}

interface WatchReply {
  path: string;
  exists: boolean;
  sha256?: string;
}

const status = ref<Status>("empty");
const error = ref("");
const limit = ref(0);
const text = ref("");
const imageUrl = ref("");
const rootRef = ref<HTMLElement | null>(null);
const renderedRef = ref<HTMLElement | null>(null);
/** Bumped by server change events; remounts PdfPreview (its cache re-keys on mtime). */
const autoTick = ref(0);

const kind = computed(() => (props.path === null ? null : kindForPath(props.path)));
const pdfKey = computed(
  () => `${props.path ?? ""}:${props.reloadTick}:${autoTick.value}:${props.trimX}x${props.trimY}`,
);
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

async function load(path: string | null, preserveScroll = false): Promise<void> {
  const scroller = preserveScroll
    ? rootRef.value?.querySelector<HTMLElement>(".preview-text, .preview-markdown")
    : null;
  const previousTop = scroller?.scrollTop ?? 0;
  status.value = path === null ? "empty" : "loading";
  error.value = "";
  text.value = "";
  imageUrl.value = "";
  if (path === null) return;
  const previewKind = kindForPath(path);
  if (previewKind === "pdf") {
    // PdfPreview fetches its own pages; the whole-file read is skipped.
    status.value = "ready";
    return;
  }
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
    if (previousTop > 0) {
      await nextTick();
      const next = rootRef.value?.querySelector<HTMLElement>(".preview-text, .preview-markdown");
      if (next !== null && next !== undefined) next.scrollTop = previousTop;
    }
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

// --- Server-side watch: one (path, watcher id) per open preview, renewed ---
const watcherId = crypto.randomUUID();
let watchedPath: string | null = null;
let lastDigest: string | null = null;
let renewTimer: ReturnType<typeof setInterval> | null = null;

function onWatchReply(reply: WatchReply): void {
  if (reply.path !== props.path) return;
  if (!reply.exists) {
    if (lastDigest !== null) status.value = "deleted";
    lastDigest = null;
    return;
  }
  const digest = reply.sha256 ?? null;
  if (lastDigest !== null && digest !== null && digest !== lastDigest) {
    // Missed events (background-tab throttle, bus reconnect): the renewal
    // heartbeat found a drift — reload like a change event would.
    autoTick.value += 1;
    void load(props.path, true);
  }
  lastDigest = digest;
}

function unwatchFile(): void {
  if (renewTimer !== null) {
    clearInterval(renewTimer);
    renewTimer = null;
  }
  if (watchedPath !== null) {
    ctx.bus
      .request("file:_:unwatch", { path: watchedPath, watcher: watcherId })
      .catch(() => undefined);
    watchedPath = null;
  }
  lastDigest = null;
}

function watchFile(path: string | null): void {
  unwatchFile();
  if (path === null) return;
  watchedPath = path;
  const renew = (): void => {
    ctx.bus
      .request("file:_:watch", { path, watcher: watcherId })
      .then((reply) => onWatchReply(reply as WatchReply))
      .catch(() => undefined);
  };
  renew();
  renewTimer = setInterval(renew, WATCH_RENEW_MS);
}

ctx.bus.subscribe("file:_:changed", (frame: BusFrame) => {
  const value = frame.value as { path?: string; exists?: boolean; sha256?: string } | null;
  if (value === null || value.path === undefined || value.path !== props.path) return;
  if (value.exists === false) {
    status.value = "deleted";
    lastDigest = null;
    return;
  }
  lastDigest = value.sha256 ?? lastDigest;
  autoTick.value += 1;
  void load(props.path, true);
});

watch(
  () => props.path,
  (path) => {
    watchFile(path);
    void load(path);
  },
  { immediate: true },
);

watch(
  () => props.reloadTick,
  () => {
    if (props.path !== null) void load(props.path, true);
  },
);

onUnmounted(unwatchFile);

// Mermaid fences need a post-render pass once the v-html is in the DOM.
watch([rendered, renderedRef], () => {
  if (rendered.value !== "") void renderMermaidIn(renderedRef.value, "files-preview");
});
</script>

<template>
  <div ref="rootRef" class="file-preview">
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
    <div v-else-if="status === 'deleted'" class="preview-notice">
      <i class="bi bi-file-earmark-x"></i>
      <div>文件已被删除或移动</div>
    </div>
    <div v-else-if="status === 'error'" class="preview-notice">
      <i class="bi bi-exclamation-triangle"></i>
      <div>{{ error }}</div>
    </div>
    <template v-else>
      <PdfPreview
        v-if="kind === 'pdf' && path !== null"
        :key="pdfKey"
        :path="path"
        :trim-x="trimX"
        :trim-y="trimY"
      />
      <div v-else-if="kind === 'image'" class="preview-image">
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
