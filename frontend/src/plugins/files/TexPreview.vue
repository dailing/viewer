<script setup lang="ts">
/**
 * LaTeX preview: renders a .tex document to HTML entirely in the browser via
 * the faster-latex WASM engine (Rust; math compiles to MathML, so fonts and
 * colours follow the viewer theme instead of a baked-in PDF look).
 *
 * Companion files (\input/\include sources, .bib for \bibliography) are read
 * through the bus and handed to the engine as a virtual file map keyed by the
 * project-relative name as written in the source. \includegraphics targets
 * are rewritten after render: browser-displayable formats go to ticket URLs
 * of the gateway's by-reference data plane (framework §6.2); PDF figures are
 * rasterized (page 1, WebP) through the file:_:pdfpage RPC like PdfPreview;
 * anything else (eps/…) becomes a link chip that opens the raw file.
 *
 * Publisher-class frontmatter the engine does not know (sn-jnl & friends:
 * \fnm/\sur, \author[marks], \title[short], \affil, \abstract{…},
 * \FloatBarrier) is normalized in a small preprocessor before rendering —
 * see normalizeFrontmatter. Citation links hover-preview the bibliography
 * entry they point at.
 *
 * The engine's version-locked article stylesheet (correct MathML spacing,
 * float/caption structure) is injected once into <head>; the scoped override
 * sheet below re-binds its --fl-* tokens, fonts, and heading sizes to the
 * viewer's theme/typography variables (framework v0.88), so 设置里的主题与
 * 字号 both apply to papers.
 */
import { inject, nextTick, onBeforeUnmount, ref, watch } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { base64ToBlob, BlobUrlOwner, fileTicketUrl, readFile, readPdfPage } from "./fileData";
import { depNames, normalizeFrontmatter, normalizeJoin } from "./texModel";
import { dirname } from "./types";

const props = defineProps<{
  path: string;
  source: string;
}>();

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("TexPreview must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const DEP_FILE_CAP = 32;
const DEP_MAX_BYTES = 1024 * 1024;
const RENDER_DEBOUNCE_MS = 250;

interface FlWarning {
  kind: string;
  message: string;
  line: number;
}

interface FlRenderResult {
  html: string;
  warnings: FlWarning[];
}

interface FlModule {
  default: () => Promise<unknown>;
  render: (source: string, options?: { files?: Record<string, string> }) => FlRenderResult;
  article_css: () => string;
}

const wrapRef = ref<HTMLElement | null>(null);
const rootRef = ref<HTMLElement | null>(null);
const html = ref("");
/** Stale-render guard: bumped per render, awaited steps bail when it moved on. */
let renderToken = 0;
let enginePromise: Promise<FlModule> | null = null;
const TICKET_CACHE_MS = 8 * 60_000; // server tickets expire after 10 minutes
const ticketCache = new Map<string, { url: string; expiresAt: number }>();

/*
 * Scroll preservation: re-renders replace the v-html tree wholesale, which
 * clamps the wrap's scrollTop to 0. Before each render of the SAME file we
 * capture an anchor (child-index path from the rendered root to the first
 * element crossing the viewport top, plus its px offset), then re-apply it
 * after the DOM swap, after image rewriting, and on late image loads — image
 * heights settle asynchronously and would otherwise push the content. Once
 * the user scrolls manually the anchor is locked off for that render cycle.
 * Path switches render fresh from the top (no anchor).
 */
interface ScrollAnchor {
  path: number[];
  /** Text key of the anchored element: robust to sibling insertions above. */
  tag: string;
  text: string;
  offset: number;
  ratio: number;
}

let lastRenderedPath = "";
let anchorLock = false;
let ignoreScrollUntil = 0;

function captureAnchor(): ScrollAnchor | null {
  const wrap = wrapRef.value;
  const root = rootRef.value;
  if (wrap === null || root === null || wrap.scrollTop === 0) return null;
  const top = wrap.getBoundingClientRect().top;
  const path: number[] = [];
  let current: Element = root;
  for (let depth = 0; depth < 8; depth++) {
    let next: Element | null = null;
    let index = 0;
    for (const child of Array.from(current.children)) {
      if (child.getBoundingClientRect().bottom > top + 2) {
        next = child;
        break;
      }
      index++;
    }
    if (next === null) break;
    path.push(index);
    current = next;
  }
  if (path.length === 0) return null;
  return {
    path,
    tag: current.tagName.toLowerCase(),
    text: (current.textContent ?? "").replace(/\s+/g, " ").trim().slice(0, 80),
    offset: current.getBoundingClientRect().top - top,
    ratio: wrap.scrollTop / Math.max(1, wrap.scrollHeight),
  };
}

function setScroll(value: number): void {
  const wrap = wrapRef.value;
  if (wrap === null) return;
  ignoreScrollUntil = performance.now() + 150;
  wrap.scrollTop = value;
}

function restoreAnchor(anchor: ScrollAnchor): void {
  const wrap = wrapRef.value;
  const root = rootRef.value;
  if (wrap === null || root === null) return;
  // Text key first: child indices shift when content is inserted above.
  if (anchor.text !== "") {
    for (const el of Array.from(root.querySelectorAll(anchor.tag))) {
      const text = (el.textContent ?? "").replace(/\s+/g, " ").trim();
      if (text.startsWith(anchor.text)) {
        const delta =
          el.getBoundingClientRect().top - wrap.getBoundingClientRect().top - anchor.offset;
        setScroll(wrap.scrollTop + delta);
        return;
      }
    }
  }
  let current: Element = root;
  for (const index of anchor.path) {
    const next = current.children.item(index);
    if (next === null) {
      setScroll(anchor.ratio * wrap.scrollHeight);
      return;
    }
    current = next;
  }
  const delta = current.getBoundingClientRect().top - wrap.getBoundingClientRect().top - anchor.offset;
  setScroll(wrap.scrollTop + delta);
}

function onUserScroll(): void {
  if (performance.now() < ignoreScrollUntil) return;
  anchorLock = true;
}

/** Re-apply the anchor as late-loading images change content height. */
function armImageLoadReanchor(token: number, anchor: ScrollAnchor): void {
  const imgs = rootRef.value?.querySelectorAll("img");
  if (imgs === null || imgs === undefined) return;
  for (const img of Array.from(imgs)) {
    if (img.complete) continue;
    img.addEventListener(
      "load",
      () => {
        if (token !== renderToken || anchorLock) return;
        restoreAnchor(anchor);
      },
      { once: true },
    );
  }
}

async function engine(): Promise<FlModule> {
  if (enginePromise !== null) return enginePromise;
  const pending = (async () => {
    const mod = (await import("@arena.ai/faster-latex/web")) as unknown as FlModule;
    await mod.default(); // fetch + instantiate the wasm (URL resolved by Vite)
    return mod;
  })();
  enginePromise = pending;
  try {
    return await pending;
  } catch (cause) {
    // A rejected promise is not a permanent engine state; the next render may
    // retry after a transient WASM download/instantiation failure.
    if (enginePromise === pending) enginePromise = null;
    throw cause;
  }
}

function injectArticleCss(css: string): void {
  const existing = document.getElementById("fl-article-css");
  if (existing?.dataset.flVersion === "0.3.1") return;
  const el = existing ?? document.createElement("style");
  el.id = "fl-article-css";
  el.dataset.flVersion = "0.3.1";
  el.textContent = css;
  if (existing === null) document.head.appendChild(el);
}

/**
 * Read the companion files referenced by the source (one \input level deep,
 * recursively). LaTeX resolves includes against the main file's directory, so
 * every read joins dirname(path). Missing files are skipped — the engine
 * renders a warning for them instead of failing.
 */
async function gatherFiles(source: string): Promise<Record<string, string>> {
  const files: Record<string, string> = {};
  const base = dirname(props.path);
  const seen = new Set<string>();
  const queue = depNames(source);
  while (queue.length > 0 && seen.size < DEP_FILE_CAP) {
    const name = queue.shift() as string;
    if (seen.has(name)) continue;
    seen.add(name);
    try {
      const result = await readFile(ctx.bus, normalizeJoin(base, name), DEP_MAX_BYTES);
      if (result.encoding !== "utf-8") continue;
      files[name] = name.endsWith(".tex") ? normalizeFrontmatter(result.content) : result.content;
      if (name.endsWith(".tex")) {
        for (const dep of depNames(result.content)) {
          if (!seen.has(dep)) queue.push(dep);
        }
      }
    } catch {
      // Missing/unreadable companion: the engine warns; the preview still renders.
    }
  }
  return files;
}

const IMAGE_EXTS = new Set(["png", "jpg", "jpeg", "gif", "webp", "avif", "bmp", "ico", "svg"]);

/** Blob URLs minted for the current render (PDF figure rasters). */
const blobUrls = new BlobUrlOwner();

function base64ToBlobUrl(base64: string, mime: string): string {
  return blobUrls.mint(base64ToBlob(base64, mime));
}

function revokeBlobs(): void {
  blobUrls.revokeAll();
}

async function ticketUrl(absPath: string): Promise<string> {
  const cached = ticketCache.get(absPath);
  if (cached !== undefined && cached.expiresAt > Date.now()) return cached.url;
  const url = await fileTicketUrl(ctx.bus, absPath);
  ticketCache.set(absPath, { url, expiresAt: Date.now() + TICKET_CACHE_MS });
  return url;
}

function replaceGraphics(img: HTMLImageElement, node: HTMLElement): void {
  img.replaceWith(node);
}

/**
 * sn-jnl & friends write \abstract/\keywords before \maketitle, so the engine
 * emits the abstract ahead of the title block. Restore the conventional
 * order: title block first, abstract after it.
 */
function fixTitleblockOrder(): void {
  const article = rootRef.value?.querySelector(".fl-article");
  if (article === null || article === undefined) return;
  const header = article.querySelector(":scope > header.fl-titleblock");
  const abstract = article.querySelector(":scope > .fl-abstract");
  if (header === null || abstract === null) return;
  // FOLLOWING = abstract sits after the header (already conventional); bail.
  if (header.compareDocumentPosition(abstract) & Node.DOCUMENT_POSITION_FOLLOWING) return;
  article.insertBefore(header, abstract);
}

/** Rasterize page 1 of a PDF figure via the fileservice pipeline (trim 0 = full page, scale 2 = sharp). */
async function pdfFigureUrl(absPath: string, token: number): Promise<string> {
  const result = await readPdfPage(ctx.bus, {
    path: absPath,
    page: 1,
    scale: 2,
    trim_x: 0,
    trim_y: 0,
  });
  if (token !== renderToken) throw new Error("stale TeX render");
  return base64ToBlobUrl(result.content, result.mime);
}

/** Point every \includegraphics at a displayable URL; PDFs rasterize, the rest become link chips. */
async function rewriteImages(token: number): Promise<void> {
  const base = dirname(props.path);
  const imgs = rootRef.value?.querySelectorAll<HTMLImageElement>("img.fl-graphics");
  if (imgs === null || imgs === undefined) return;
  revokeBlobs(); // previous render's DOM (and its blob URLs) is gone
  for (const img of Array.from(imgs)) {
    const src = img.getAttribute("src") ?? "";
    if (src === "" || /^(https?:|data:|blob:)/.test(src)) continue;
    const abs = normalizeJoin(base, src);
    const ext = abs.includes(".") ? abs.slice(abs.lastIndexOf(".") + 1).toLowerCase() : "";
    try {
      if (ext === "pdf") {
        const url = await pdfFigureUrl(abs, token);
        if (token !== renderToken) return;
        img.src = url;
        continue;
      }
      const url = await ticketUrl(abs);
      if (token !== renderToken) return;
      if (IMAGE_EXTS.has(ext)) {
        img.src = url;
      } else {
        const link = document.createElement("a");
        link.className = "fl-graphics-link";
        link.href = url;
        link.target = "_blank";
        link.rel = "noreferrer";
        link.textContent = `🗎 ${src}（${ext === "" ? "文件" : ext.toUpperCase()} 图，点击在新窗口打开）`;
        replaceGraphics(img, link);
      }
    } catch {
      if (token !== renderToken) return;
      const missing = document.createElement("span");
      missing.className = "fl-graphics-missing";
      missing.textContent = `⚠ 图片无法读取：${src}`;
      replaceGraphics(img, missing);
    }
  }
}

/*
 * Citation hover: engine renders \citep & friends as <a href="#bib-KEY">
 * and the bibliography as <dt id="bib-KEY">[n]</dt><dd>entry</dd>. Hovering
 * a citation link shows the referenced <dd>'s full text in a tooltip so the
 * entry can be checked without jumping to the bibliography.
 */
const citeTip = ref<{ text: string; x: number; y: number } | null>(null);

function onCiteHover(event: MouseEvent): void {
  const target = event.target instanceof Element ? event.target.closest('a[href^="#bib-"]') : null;
  if (target === null || rootRef.value === null) return;
  const id = target.getAttribute("href")?.slice(1) ?? "";
  const dt = rootRef.value.querySelector(`[id="${CSS.escape(id)}"]`);
  const dd = dt?.nextElementSibling;
  const text = dd?.textContent?.replace(/\s+/g, " ").trim() ?? "";
  if (text === "") return;
  const rect = target.getBoundingClientRect();
  const x = Math.min(rect.left, Math.max(12, window.innerWidth - 492));
  citeTip.value = { text, x, y: rect.bottom + 6 };
}

function onCiteLeave(event: MouseEvent): void {
  if (citeTip.value === null) return;
  const from = event.target instanceof Element ? event.target.closest('a[href^="#bib-"]') : null;
  if (from !== null) citeTip.value = null;
}

async function renderNow(): Promise<void> {
  const token = ++renderToken;
  anchorLock = false;
  const anchor = props.path === lastRenderedPath ? captureAnchor() : null;
  try {
    const mod = await engine();
    if (token !== renderToken) return;
    injectArticleCss(mod.article_css());
    const files = await gatherFiles(props.source);
    if (token !== renderToken) return;
    const out = mod.render(normalizeFrontmatter(props.source), { files });
    html.value = out.html;
    if (out.warnings.length > 0) console.debug("[tex] render warnings", out.warnings);
    await nextTick();
    if (token !== renderToken) return;
    lastRenderedPath = props.path;
    fixTitleblockOrder();
    if (anchor !== null) restoreAnchor(anchor);
    await rewriteImages(token);
    if (token !== renderToken) return;
    if (anchor !== null && !anchorLock) {
      restoreAnchor(anchor);
      armImageLoadReanchor(token, anchor);
      // MathML webfonts (Latin Modern Math & friends) settle asynchronously
      // after the DOM swap and shift equation heights — re-anchor once they do.
      if (document.fonts.status === "loading") {
        void document.fonts.ready.then(() => {
          if (token !== renderToken || anchorLock) return;
          restoreAnchor(anchor);
        });
      }
    }
  } catch (cause) {
    if (token !== renderToken) return;
    console.warn("[tex] render failed", cause);
  }
}

let debounceTimer: number | null = null;
watch(
  () => [props.source, props.path],
  () => {
    if (debounceTimer !== null) window.clearTimeout(debounceTimer);
    debounceTimer = window.setTimeout(() => {
      debounceTimer = null;
      void renderNow();
    }, RENDER_DEBOUNCE_MS);
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  renderToken++;
  if (debounceTimer !== null) {
    window.clearTimeout(debounceTimer);
    debounceTimer = null;
  }
  revokeBlobs();
  ticketCache.clear();
});
</script>

<template>
  <div
    ref="wrapRef"
    class="preview-latex-wrap"
    @mouseover="onCiteHover"
    @mouseout="onCiteLeave"
    @scroll.passive="onUserScroll"
  >
    <div v-if="html === ''" class="preview-latex-loading">正在渲染 LaTeX…</div>
    <div v-else ref="rootRef" class="preview-latex" v-html="html"></div>
    <div
      v-if="citeTip !== null"
      class="preview-latex-citetip"
      :style="{ left: `${citeTip.x}px`, top: `${citeTip.y}px` }"
    >
      {{ citeTip.text }}
    </div>
  </div>
</template>

<style>
/*
 * Theme bridge for the engine's article stylesheet (injected at runtime):
 * re-bind its --fl-* semantic tokens to the viewer palette and its fonts and
 * heading sizes to the v0.88 typography variables. These rules must be
 * non-scoped — the rendered tree arrives via v-html and carries no scope id.
 * All selectors sit under .preview-latex so nothing leaks into the shell.
 */
.preview-latex-wrap {
  background: var(--color-surface);
  height: 100%;
  overflow: auto;
}

.preview-latex-loading {
  color: var(--color-text-subtle);
  font-size: var(--font-size-ui-small);
  padding: 24px;
  text-align: center;
}

.preview-latex .fl-article {
  --fl-maxw: none;
  --fl-fg: var(--color-text);
  --fl-fg-muted-strong: var(--color-text-muted);
  --fl-fg-muted: var(--color-text-muted);
  --fl-fg-subtle: var(--color-text-subtle);
  --fl-fg-faint: var(--color-text-subtle);
  --fl-link: var(--color-accent);
  --fl-surface: var(--color-surface-raised);
  --fl-surface-2: var(--color-surface-raised);
  --fl-stripe: var(--color-surface-raised);
  --fl-border: var(--color-border);
  --fl-border-faint: var(--color-border);
  --fl-rule: var(--color-border-strong);
  --fl-box-border: var(--color-border-strong);
  --fl-accent: var(--color-accent);
  --fl-accent-fg: var(--color-canvas);
  --fl-danger: var(--color-danger);
  --fl-danger-bg: var(--color-surface-raised);
  --fl-danger-border: var(--color-danger);

  font-family: inherit;
  font-size: var(--markdown-body-font-size);
  line-height: var(--markdown-body-line-height);
  max-width: none;
  padding: 12px 20px 48px;
}

.preview-latex .fl-title,
.preview-latex .fl-part,
.preview-latex .fl-section,
.preview-latex .fl-toc-title,
.preview-latex .fl-bib-title {
  font-family: inherit;
}

.preview-latex .fl-title {
  font-size: var(--markdown-h1-font-size);
}

.preview-latex .fl-part,
.preview-latex .fl-level-0 {
  font-size: calc(var(--markdown-h1-font-size) * 15 / 14);
}

.preview-latex h2.fl-section {
  font-size: var(--markdown-h2-font-size);
}

.preview-latex h3.fl-section {
  font-size: var(--markdown-h3-font-size);
}

.preview-latex h4.fl-section,
.preview-latex h5.fl-section,
.preview-latex h6.fl-section {
  font-size: var(--markdown-h4-font-size);
}

.preview-latex .fl-toc-title,
.preview-latex .fl-bib-title {
  font-size: var(--markdown-h3-font-size);
}

/* rem-based engine sizes re-expressed in em so they track the content base. */
.preview-latex .fl-author {
  font-size: 1.1em;
}

.preview-latex .fl-date,
.preview-latex .fl-affiliation,
.preview-latex .fl-thanks,
.preview-latex .fl-email,
.preview-latex .fl-keywords,
.preview-latex .fl-subjclass,
.preview-latex .fl-ccs,
.preview-latex .fl-abstract,
.preview-latex .fl-footnotes,
.preview-latex .fl-caption {
  font-size: 0.92em;
}

.preview-latex .fl-subtitle {
  font-size: 1.2em;
}

.preview-latex .fl-graphics-link,
.preview-latex .fl-graphics-missing {
  color: var(--fl-fg-subtle);
  display: inline-block;
  font-size: 0.9em;
  padding: 8px 12px;
}

.preview-latex a.fl-graphics-link {
  border: 1px dashed var(--fl-border);
  border-radius: 6px;
  color: var(--fl-link);
  text-decoration: none;
}

.preview-latex-citetip {
  background: var(--color-surface-raised);
  border: 1px solid var(--color-border-strong);
  border-radius: 6px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.35);
  color: var(--color-text);
  font-size: var(--font-size-ui-small);
  line-height: 1.5;
  max-width: 480px;
  padding: 8px 12px;
  pointer-events: none;
  position: fixed;
  z-index: 100;
}
</style>
