<script lang="ts">
/* Module scope (runs once per app load, shared by every PdfPreview instance
 * — <script setup> top-level state would be PER-INSTANCE): scroll memory
 * across refresh remounts. FilePreview remounts this component on every
 * server change event / manual refresh (fresh rasters key off the new
 * mtime), which would reset the scroll position to the top. Positions are
 * remembered per path as (anchor page, fractional offset into the page), so
 * the restore survives page aspect corrections landing after it. */
const pdfScrollMemory = new Map<string, { page: number; ratio: number }>();
</script>

<script setup lang="ts">
/**
 * PDF preview: per-page WebP rasters via the file:_:pdfpage RPC. Three
 * resolution tiers (low = instant, high = sharp, ultra = print) escalate
 * sequentially on visible pages, with adjacent-page low-tier prefetch; a
 * module-level LRU (blob object URLs, byte-budgeted) is shared across panes
 * so reopened pages are free. White margins are trimmed server-side; the
 * reported image dimensions correct each page's aspect placeholder.
 */
import { inject, nextTick, onBeforeUnmount, ref, watch } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { PdfPageResult } from "./types";

const props = defineProps<{ path: string }>();

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("PdfPreview must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const LOW_SCALE = 1;
const HIGH_SCALE = 2;
const ULTRA_SCALE = 3;
const PREFETCH_AHEAD = 2;
const CACHE_BYTES_MAX = 512 * 1024 * 1024;
const DEFAULT_WIDTH = 612;
const DEFAULT_HEIGHT = 792;

type TierField = "low" | "high" | "ultra";
type TierFlag = "lowRequested" | "highRequested" | "ultraRequested";

interface PageState {
  width: number;
  height: number;
  low: string | null;
  high: string | null;
  ultra: string | null;
  lowRequested: boolean;
  highRequested: boolean;
  ultraRequested: boolean;
  error: string | null;
}

// Page raster cache: key = `${path}@${mtime}:${page}:${scale}`. NOTE: in
// <script setup> this is PER-INSTANCE, not module-level — cross-instance and
// cross-remount reuse is backed by the server-side disk LRU.
const pageCache = new Map<string, { url: string; bytes: number }>();
let cacheBytes = 0;
const inflight = new Map<string, Promise<string>>();

function cacheGet(key: string): string | null {
  const entry = pageCache.get(key);
  if (entry === undefined) return null;
  pageCache.delete(key);
  pageCache.set(key, entry); // refresh LRU position
  return entry.url;
}

function cacheSet(key: string, url: string, bytes: number): void {
  const replaced = pageCache.get(key);
  if (replaced !== undefined) {
    pageCache.delete(key);
    cacheBytes -= replaced.bytes;
    URL.revokeObjectURL(replaced.url);
  }
  while (cacheBytes + bytes > CACHE_BYTES_MAX && pageCache.size > 0) {
    const oldest = pageCache.keys().next().value as string;
    const evicted = pageCache.get(oldest);
    pageCache.delete(oldest);
    if (evicted !== undefined) {
      cacheBytes -= evicted.bytes;
      URL.revokeObjectURL(evicted.url);
    }
  }
  pageCache.set(key, { url, bytes });
  cacheBytes += bytes;
}

function base64ToBlobUrl(content: string, mime: string): { url: string; bytes: number } {
  const binary = atob(content);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
  const blob = new Blob([bytes], { type: mime });
  return { url: URL.createObjectURL(blob), bytes: blob.size };
}

const loading = ref(true);
const loadError = ref("");
const pages = ref(0);
const mtime = ref(0);
const pageStates = ref<PageState[]>([]);
const scrollRef = ref<HTMLElement | null>(null);
let generation = 0;
let observer: IntersectionObserver | null = null;

function saveScroll(): void {
  const scroller = scrollRef.value;
  if (scroller === null) return;
  const scrollerRect = scroller.getBoundingClientRect();
  for (const element of scroller.querySelectorAll<HTMLElement>(".pdf-page")) {
    const rect = element.getBoundingClientRect();
    const contentTop = rect.top - scrollerRect.top + scroller.scrollTop;
    if (contentTop + rect.height > scroller.scrollTop && rect.height > 0) {
      pdfScrollMemory.set(props.path, {
        page: Number(element.dataset.page ?? 1),
        ratio: (scroller.scrollTop - contentTop) / rect.height,
      });
      return;
    }
  }
  // No page elements rendered (loading/error notice after a transient bad
  // write): keep the previous memory so the next successful reload still
  // restores the position.
}

function restoreScroll(): void {
  const scroller = scrollRef.value;
  const saved = pdfScrollMemory.get(props.path);
  if (scroller === null || saved === undefined) return;
  const scrollerRect = scroller.getBoundingClientRect();
  for (const element of scroller.querySelectorAll<HTMLElement>(".pdf-page")) {
    if (Number(element.dataset.page ?? 0) === saved.page) {
      const rect = element.getBoundingClientRect();
      scroller.scrollTop = rect.top - scrollerRect.top + scroller.scrollTop + saved.ratio * rect.height;
      return;
    }
  }
  // The anchor page lies beyond the (shorter) new document: park at the end.
  scroller.scrollTop = scroller.scrollHeight;
}

function cacheKey(page: number, scale: number): string {
  return `${props.path}@${mtime.value === 0 ? "open" : mtime.value}:${page}:${scale}`;
}

function adoptMeta(result: PdfPageResult): void {
  mtime.value = result.mtime;
  if (pages.value !== result.pages) {
    pages.value = result.pages;
    pageStates.value = Array.from({ length: result.pages }, () => ({
      width: result.image_width > 0 ? result.image_width : result.page_width,
      height: result.image_height > 0 ? result.image_height : result.page_height,
      low: null,
      high: null,
      ultra: null,
      lowRequested: false,
      highRequested: false,
      ultraRequested: false,
      error: null,
    }));
  }
  const state = pageStates.value[result.page - 1];
  // The trimmed raster's aspect beats the mediabox estimate, but only adopt
  // it before the page shows anything so later tiers can't wobble the layout.
  if (state !== undefined && state.low === null && result.image_width > 0) {
    state.width = result.image_width;
    state.height = result.image_height;
  }
}

async function fetchPage(page: number, scale: number): Promise<string> {
  const key = cacheKey(page, scale);
  const hit = cacheGet(key);
  if (hit !== null) return hit;
  const pending = inflight.get(key);
  if (pending !== undefined) return pending;
  const request = (async (): Promise<string> => {
    const result = (await ctx.bus.request("file:_:pdfpage", {
      path: props.path,
      page,
      scale,
    })) as PdfPageResult;
    adoptMeta(result);
    const { url, bytes } = base64ToBlobUrl(result.content, result.mime);
    cacheSet(key, url, bytes);
    return url;
  })().finally(() => inflight.delete(key));
  inflight.set(key, request);
  return request;
}

function tierField(scale: number): TierField {
  return scale === LOW_SCALE ? "low" : scale === HIGH_SCALE ? "high" : "ultra";
}

function tierFlag(scale: number): TierFlag {
  return scale === LOW_SCALE ? "lowRequested" : scale === HIGH_SCALE ? "highRequested" : "ultraRequested";
}

async function ensureTier(page: number, scale: number, gen: number): Promise<void> {
  const state = pageStates.value[page - 1];
  if (state === undefined || state.error !== null) return;
  const field = tierField(scale);
  const flag = tierFlag(scale);
  if (state[field] !== null || state[flag]) return;
  state[flag] = true;
  try {
    const url = await fetchPage(page, scale);
    if (gen !== generation) return;
    const current = pageStates.value[page - 1];
    if (current !== undefined) current[field] = url;
  } catch (cause) {
    if (gen !== generation) return;
    const current = pageStates.value[page - 1];
    // Only a page with nothing to show reports an error; a failed upgrade
    // silently keeps the tier it already has.
    if (current !== undefined && current.low === null && current.high === null) {
      current.error = cause instanceof Error ? cause.message : "页面渲染失败";
    }
  }
}

function onIntersect(entries: IntersectionObserverEntry[]): void {
  const gen = generation;
  for (const entry of entries) {
    const page = Number((entry.target as HTMLElement).dataset.page);
    if (!entry.isIntersecting) continue;
    void ensureTier(page, LOW_SCALE, gen);
    // Ultra escalates only after the high tier landed, keeping wire priority
    // for first-paint tiers of this and neighbouring pages.
    void ensureTier(page, HIGH_SCALE, gen).then(() => {
      if (gen === generation) void ensureTier(page, ULTRA_SCALE, gen);
    });
    for (let ahead = 1; ahead <= PREFETCH_AHEAD; ahead++) {
      if (page + ahead <= pages.value) void ensureTier(page + ahead, LOW_SCALE, gen);
    }
  }
}

function observePages(): void {
  observer?.disconnect();
  observer = new IntersectionObserver(onIntersect, {
    root: scrollRef.value,
    rootMargin: "150% 0px",
  });
  scrollRef.value?.querySelectorAll(".pdf-page").forEach((element) => observer?.observe(element));
}

// Re-observe when the page list changes AND when loading flips false: the
// v-else branch mounting the .pdf-page divs renders only after loading=false,
// which lands in a later flush than the adoptMeta pages update.
watch([pages, loading], () => void nextTick(observePages));

async function load(path: string, gen: number): Promise<void> {
  loading.value = true;
  loadError.value = "";
  pages.value = 0;
  mtime.value = 0;
  pageStates.value = [];
  try {
    const url = await fetchPage(1, LOW_SCALE);
    if (gen !== generation) return;
    const first = pageStates.value[0];
    if (first !== undefined) {
      first.low = url;
      first.lowRequested = true;
    }
  } catch (cause) {
    if (gen !== generation) return;
    loadError.value = cause instanceof Error ? cause.message : "PDF 加载失败";
  } finally {
    if (gen === generation) {
      loading.value = false;
      // Page divs mount with their aspect placeholders in the same flush, so
      // the anchor restore lands on correct offsets immediately.
      void nextTick(() => {
        if (gen === generation) restoreScroll();
      });
    }
  }
}

watch(
  () => props.path,
  (path) => {
    const gen = ++generation;
    void load(path, gen);
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  saveScroll();
  generation++;
  observer?.disconnect();
});

function ratioFor(state: PageState): string {
  return `${state.width || DEFAULT_WIDTH} / ${state.height || DEFAULT_HEIGHT}`;
}
</script>

<template>
  <div ref="scrollRef" class="pdf-preview">
    <div v-if="loading" class="preview-notice">
      <i class="bi bi-arrow-repeat"></i>
      <div>正在加载…</div>
    </div>
    <div v-else-if="loadError !== ''" class="preview-notice">
      <i class="bi bi-exclamation-triangle"></i>
      <div>{{ loadError }}</div>
    </div>
    <template v-else>
      <div
        v-for="(state, index) in pageStates"
        :key="index"
        class="pdf-page"
        :data-page="index + 1"
        :style="{ aspectRatio: ratioFor(state) }"
      >
        <img
          v-if="(state.ultra ?? state.high ?? state.low) !== null"
          :src="(state.ultra ?? state.high ?? state.low) as string"
          :class="{ upgrading: state.ultra === null }"
          :alt="`第 ${index + 1} 页`"
        />
        <div v-else-if="state.error !== null" class="pdf-page-error">{{ state.error }}</div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.pdf-preview {
  background: var(--color-surface);
  height: 100%;
  overflow: auto;
  padding: 12px;
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
}

.pdf-page {
  background: #fff;
  box-shadow: 0 1px 4px rgb(0 0 0 / 25%);
  margin: 0 auto 12px;
  max-width: 100%;
  overflow: hidden;
  position: relative;
  width: 100%;
}

.pdf-page img {
  display: block;
  height: 100%;
  width: 100%;
}

.pdf-page img.upgrading {
  image-rendering: auto;
  opacity: 0.92;
}

.pdf-page-error {
  align-items: center;
  color: var(--color-text-subtle);
  display: flex;
  font-size: var(--font-size-ui-small);
  height: 100%;
  justify-content: center;
  padding: 12px;
  text-align: center;
}
</style>
