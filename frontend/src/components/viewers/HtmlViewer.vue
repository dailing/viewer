<script setup lang="ts">
import hljs from "highlight.js";
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { getText, resolveMarkdownLink, siteUrl } from "../../api/client";
import { useReloadingScrollMemory } from "../../composables/useScrollMemory";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import type { WatchEvent } from "../../types/files";
import { isLocalLinkTarget } from "../../utils/markdownRender";
import { fileChangeAffectsPath, parentPath } from "../../utils/paths";
import { restoreScrollPosition } from "../../utils/scrollMemory";

type HtmlMode = "rendered" | "raw";

const props = defineProps<{ path: string; version: number; paneId: string; workspaceId: string; contentHash?: string }>();
const toolbar = usePaneToolbarStore();
const mode = ref<HtmlMode>("rendered");
const text = ref("");
const rawError = ref("");
const frameVersion = ref(0);
const dependencies = ref<Set<string>>(new Set());
const container = ref<HTMLElement | null>(null);

const frameSrc = computed(() => siteUrl(props.path, `${props.contentHash ?? props.version}-${frameVersion.value}`));
const highlightedRaw = computed(() => {
  if (!text.value) return "";
  return hljs.highlight(text.value, { language: "xml", ignoreIllegals: true }).value;
});

function setMode(value: HtmlMode) {
  mode.value = value;
  registerToolbar();
  void nextTick(() => restoreScrollPosition(scrollTarget(), container.value));
}

function reloadFrame() {
  frameVersion.value += 1;
}

function registerToolbar() {
  toolbar.setPaneToolbar(props.paneId, {
    title: props.path,
    actions: [
      { id: "html-reload", title: "Reload HTML", icon: "bi-arrow-clockwise", run: load },
      { id: "html-rendered", title: "Rendered HTML", label: "Rendered", active: mode.value === "rendered", run: () => setMode("rendered") },
      { id: "html-raw", title: "Raw HTML", label: "Raw", active: mode.value === "raw", run: () => setMode("raw") },
      {
        id: "html-open",
        title: "Open HTML in new tab",
        icon: "bi-box-arrow-up-right",
        run: () => {
          window.open(frameSrc.value, "_blank", "noreferrer");
        },
      },
    ],
  });
}

function attributeTargets(source: string, attribute: string): string[] {
  const pattern = new RegExp(`\\b${attribute}\\s*=\\s*(?:"([^"]*)"|'([^']*)'|([^\\s>]+))`, "gi");
  const targets: string[] = [];
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(source))) {
    const target = match[1] ?? match[2] ?? match[3] ?? "";
    if (target) targets.push(target);
  }
  return targets;
}

function srcsetTargets(source: string): string[] {
  return attributeTargets(source, "srcset").flatMap((value) =>
    value
      .split(",")
      .map((part) => part.trim().split(/\s+/)[0])
      .filter(Boolean),
  );
}

function htmlDependencyTargets(source: string): string[] {
  const targets = [
    ...attributeTargets(source, "src"),
    ...attributeTargets(source, "href"),
    ...attributeTargets(source, "poster"),
    ...attributeTargets(source, "data"),
    ...srcsetTargets(source),
  ];
  return [...new Set(targets.filter(isLocalLinkTarget))];
}

async function updateDependencies(source: string) {
  const resolved = await Promise.allSettled(htmlDependencyTargets(source).map(resolveHtmlDependency));
  dependencies.value = new Set(
    resolved
      .filter((result): result is PromiseFulfilledResult<{ path: string }> => result.status === "fulfilled")
      .map((result) => result.value.path),
  );
}

async function resolveHtmlDependency(target: string): Promise<{ path: string }> {
  if (target.startsWith("/") && !target.startsWith("//")) {
    const path = target.split(/[?#]/, 1)[0].replace(/^\/+/, "");
    try {
      return { path: decodeURIComponent(path) };
    } catch {
      return { path };
    }
  }
  return resolveMarkdownLink(props.path, target);
}

async function load() {
  rawError.value = "";
  reloadFrame();
  try {
    text.value = await getText(props.path);
    await updateDependencies(text.value);
  } catch (err) {
    text.value = "";
    dependencies.value = new Set();
    rawError.value = err instanceof Error ? err.message : String(err);
  }
  registerToolbar();
  await nextTick();
  await restoreScrollPosition(scrollTarget(), container.value);
}

function isIndexHtmlPath(path: string): boolean {
  return path.split("/").pop()?.toLowerCase() === "index.html";
}

function isInsideIndexDirectory(eventPath: string): boolean {
  if (!isIndexHtmlPath(props.path)) return false;
  const base = parentPath(props.path);
  return base ? eventPath.startsWith(`${base}/`) : !eventPath.includes("/");
}

function handleFileChanged(event: Event) {
  const detail = (event as CustomEvent<WatchEvent>).detail;
  if (
    fileChangeAffectsPath(detail.path, props.path) ||
    dependencies.value.has(detail.path) ||
    [...dependencies.value].some((path) => fileChangeAffectsPath(detail.path, path)) ||
    isInsideIndexDirectory(detail.path)
  ) {
    void load();
  }
}

const { saveCurrentScroll } = useReloadingScrollMemory(
  () => props.path,
  () => props.version,
  container,
  load,
  () => ({ paneId: props.paneId, workspaceId: props.workspaceId }),
);

function scrollTarget() {
  return { path: props.path, paneId: props.paneId, workspaceId: props.workspaceId };
}

watch(() => props.contentHash, load);
onMounted(() => {
  window.addEventListener("viewer:file-changed", handleFileChanged);
  registerToolbar();
});
onUnmounted(() => {
  window.removeEventListener("viewer:file-changed", handleFileChanged);
  toolbar.clearPaneToolbar(props.paneId);
});
</script>

<template>
  <div v-if="mode === 'rendered'" class="html-viewer-shell">
    <iframe
      class="html-frame"
      :src="frameSrc"
      title="Rendered HTML preview"
      sandbox="allow-scripts allow-same-origin allow-forms allow-modals allow-popups allow-downloads"
    ></iframe>
  </div>
  <pre v-else-if="!rawError" ref="container" class="html-raw hljs markdown-syntax" @scroll.passive="saveCurrentScroll"><code v-html="highlightedRaw"></code></pre>
  <div v-else class="html-error">{{ rawError }}</div>
</template>

<style scoped>
.html-viewer-shell {
  background: #ffffff;
  height: 100%;
  min-height: 0;
}

.html-frame {
  background: #ffffff;
  border: 0;
  display: block;
  height: 100%;
  width: 100%;
}

.html-raw {
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

.html-error {
  color: #a33;
  padding: 14px;
}
</style>
