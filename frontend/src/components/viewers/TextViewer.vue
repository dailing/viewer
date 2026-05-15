<script setup lang="ts">
import hljs from "highlight.js";
import "highlight.js/styles/github.css";
import { nextTick, onUnmounted, ref } from "vue";
import { getText } from "../../api/client";
import { useReloadingScrollMemory } from "../../composables/useScrollMemory";
import { restoreScrollPosition } from "../../utils/scrollMemory";

const props = defineProps<{ path: string; version: number; paneId: string; workspaceId: string }>();
const text = ref("");
const highlightedHtml = ref("");
const error = ref("");
const copied = ref(false);
const container = ref<HTMLElement | null>(null);
let copiedTimer: number | undefined;

const extensionLanguages: Record<string, string> = {
  bash: "bash",
  c: "c",
  cc: "cpp",
  cfg: "ini",
  conf: "ini",
  cpp: "cpp",
  cs: "csharp",
  css: "css",
  csv: "csv",
  diff: "diff",
  go: "go",
  h: "cpp",
  hpp: "cpp",
  html: "xml",
  ini: "ini",
  java: "java",
  js: "javascript",
  json: "json",
  jsx: "javascript",
  kt: "kotlin",
  log: "plaintext",
  lua: "lua",
  patch: "diff",
  php: "php",
  py: "python",
  rb: "ruby",
  rs: "rust",
  scss: "scss",
  sh: "bash",
  sql: "sql",
  swift: "swift",
  toml: "ini",
  ts: "typescript",
  tsx: "typescript",
  txt: "plaintext",
  vue: "xml",
  xml: "xml",
  yaml: "yaml",
  yml: "yaml",
  zsh: "bash",
};

const filenameLanguages: Record<string, string> = {
  dockerfile: "dockerfile",
  makefile: "makefile",
};

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function languageForPath(path: string): string | null {
  const filename = path.split("/").pop()?.toLowerCase() ?? "";
  if (filenameLanguages[filename]) return filenameLanguages[filename];
  const extension = filename.includes(".") ? filename.split(".").pop() ?? "" : "";
  return extensionLanguages[extension] ?? null;
}

function highlightText(value: string): string {
  const language = languageForPath(props.path);
  if (language === "plaintext") return escapeHtml(value);
  if (language && hljs.getLanguage(language)) {
    return hljs.highlight(value, { language, ignoreIllegals: true }).value;
  }
  return hljs.highlightAuto(value).value;
}

async function copyAll() {
  try {
    await navigator.clipboard.writeText(text.value);
  } catch {
    const textarea = document.createElement("textarea");
    textarea.value = text.value;
    textarea.setAttribute("readonly", "");
    textarea.style.position = "fixed";
    textarea.style.left = "-9999px";
    document.body.appendChild(textarea);
    textarea.select();
    document.execCommand("copy");
    document.body.removeChild(textarea);
  }
  copied.value = true;
  window.clearTimeout(copiedTimer);
  copiedTimer = window.setTimeout(() => {
    copied.value = false;
  }, 1400);
}

async function load() {
  error.value = "";
  try {
    text.value = await getText(props.path);
    highlightedHtml.value = highlightText(text.value);
    await nextTick();
    await restoreScrollPosition(scrollTarget(), container.value);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
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

onUnmounted(() => {
  window.clearTimeout(copiedTimer);
});
</script>

<template>
  <div v-if="!error" class="text-viewer-shell">
    <button class="copy-button" type="button" :title="copied ? 'Copied' : 'Copy all text'" @click="copyAll">
      <i :class="copied ? 'bi bi-check2' : 'bi bi-clipboard'"></i>
    </button>
    <pre ref="container" class="text-viewer hljs markdown-syntax" @scroll.passive="saveCurrentScroll"><code v-html="highlightedHtml"></code></pre>
  </div>
  <div v-else class="text-error">{{ error }}</div>
</template>

<style scoped>
.text-viewer-shell {
  height: 100%;
  min-height: 0;
  position: relative;
}

.copy-button {
  align-items: center;
  background: rgb(255 255 255 / 0.9);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: #344054;
  display: inline-flex;
  height: 32px;
  justify-content: center;
  position: absolute;
  right: 12px;
  top: 10px;
  width: 32px;
  z-index: 2;
}

.copy-button:hover {
  background: #ffffff;
  border-color: #b8c0cc;
}

.text-viewer {
  background: var(--syntax-background);
  color: var(--syntax-text);
  height: 100%;
  margin: 0;
  overflow: auto;
  padding: 14px 54px 14px 14px;
  user-select: text;
  white-space: pre-wrap;
  word-break: break-word;
}

.text-error {
  color: #a33;
  padding: 14px;
}
</style>
