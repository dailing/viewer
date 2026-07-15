<script setup lang="ts">
import hljs from "highlight.js";
import "highlight.js/styles/github.css";
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import { getText, putText } from "../../api/client";
import { useReloadingScrollMemory } from "../../composables/useScrollMemory";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { restoreScrollPosition } from "../../utils/scrollMemory";

const props = defineProps<{ path: string; version: number; paneId: string; workspaceId: string }>();
const toolbar = usePaneToolbarStore();
const text = ref("");
const draft = ref("");
const highlightedHtml = ref("");
const error = ref("");
const copied = ref(false);
const isEditing = ref(false);
const saving = ref(false);
const container = ref<HTMLElement | null>(null);
const editPreview = ref<HTMLElement | null>(null);
const syncingEditScroll = ref(false);
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

const highlightedDraftHtml = computed(() => highlightText(draft.value));

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function languageForPath(path: string): string | null {
  const filename = path.split("/").pop()?.toLowerCase() ?? "";
  if (filename === ".env" || filename.startsWith(".env.") || filename.endsWith(".env")) return "bash";
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

function registerToolbar() {
  toolbar.setPaneToolbar(props.paneId, {
    title: props.path,
    actions: [
      { id: "text-edit", title: isEditing.value ? "Editing Text" : "Edit Text", icon: "bi-pencil-square", active: isEditing.value, run: toggleEdit },
      { id: "text-copy", title: copied.value ? "Copied" : "Copy all text", icon: copied.value ? "bi-check2" : "bi-clipboard", run: copyAll },
    ],
  });
}

function toggleEdit() {
  if (isEditing.value) {
    cancelEdit();
    return;
  }
  draft.value = text.value;
  isEditing.value = true;
  registerToolbar();
  void nextTick(() => syncScroll(container.value, editPreview.value));
}

function cancelEdit() {
  draft.value = text.value;
  isEditing.value = false;
  error.value = "";
  registerToolbar();
  void nextTick(() => restoreScrollPosition(scrollTarget(), container.value));
}

async function saveEdit() {
  if (saving.value) return;
  saving.value = true;
  error.value = "";
  try {
    await putText(props.path, draft.value);
    text.value = draft.value;
    highlightedHtml.value = highlightText(text.value);
    isEditing.value = false;
    registerToolbar();
    await nextTick();
    await restoreScrollPosition(scrollTarget(), container.value);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    saving.value = false;
  }
}

function syncScroll(source: HTMLElement | null, target: HTMLElement | null) {
  if (!source || !target || syncingEditScroll.value) return;
  const sourceRange = source.scrollHeight - source.clientHeight;
  const targetRange = target.scrollHeight - target.clientHeight;
  if (sourceRange <= 0 || targetRange <= 0) return;
  syncingEditScroll.value = true;
  target.scrollTop = (source.scrollTop / sourceRange) * targetRange;
  window.requestAnimationFrame(() => {
    syncingEditScroll.value = false;
  });
}

function onEditorScroll() {
  saveCurrentScroll();
  if (isEditing.value) syncScroll(container.value, editPreview.value);
}

function onEditPreviewScroll() {
  syncScroll(editPreview.value, container.value);
}

async function copyAll() {
  const value = isEditing.value ? draft.value : text.value;
  try {
    await navigator.clipboard.writeText(value);
  } catch {
    const textarea = document.createElement("textarea");
    textarea.value = value;
    textarea.setAttribute("readonly", "");
    textarea.style.position = "fixed";
    textarea.style.left = "-9999px";
    document.body.appendChild(textarea);
    textarea.select();
    document.execCommand("copy");
    document.body.removeChild(textarea);
  }
  copied.value = true;
  registerToolbar();
  window.clearTimeout(copiedTimer);
  copiedTimer = window.setTimeout(() => {
    copied.value = false;
    registerToolbar();
  }, 1400);
}

async function load() {
  if (isEditing.value) return;
  error.value = "";
  try {
    text.value = await getText(props.path);
    highlightedHtml.value = highlightText(text.value);
    registerToolbar();
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

watch(draft, () => {
  if (isEditing.value) void nextTick(() => syncScroll(container.value, editPreview.value));
});

onUnmounted(() => {
  window.clearTimeout(copiedTimer);
  toolbar.clearPaneToolbar(props.paneId);
});
</script>

<template>
  <div v-if="isEditing" class="text-editor">
    <div class="text-editor-workspace">
      <textarea
        ref="container"
        v-model="draft"
        class="text-editor-input"
        spellcheck="false"
        @scroll.passive="onEditorScroll"
      ></textarea>
      <pre
        ref="editPreview"
        class="text-viewer text-editor-preview hljs markdown-syntax"
        @scroll.passive="onEditPreviewScroll"
      ><code v-html="highlightedDraftHtml"></code></pre>
    </div>
    <div class="text-editor-actions">
      <button class="btn btn-sm btn-primary" type="button" :disabled="saving" @click="saveEdit">
        <i class="bi" :class="saving ? 'bi-arrow-repeat' : 'bi-check2'"></i>
        <span>{{ saving ? "Saving" : "Save" }}</span>
      </button>
      <button class="btn btn-sm btn-outline-secondary" type="button" :disabled="saving" @click="cancelEdit">
        <i class="bi bi-x-lg"></i>
        <span>Cancel</span>
      </button>
    </div>
    <div v-if="error" class="text-error">{{ error }}</div>
  </div>
  <div v-else-if="!error" class="text-viewer-shell">
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

.text-viewer {
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

.text-editor {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.text-editor-workspace {
  display: grid;
  flex: 1 1 auto;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  min-height: 0;
}

.text-editor-input {
  background: var(--syntax-background);
  border: 0;
  color: var(--syntax-text);
  flex: 1 1 auto;
  font-family: var(--bs-font-monospace);
  font-size: 13px;
  line-height: 1.5;
  min-height: 0;
  outline: none;
  padding: 14px;
  resize: none;
  width: 100%;
}

.text-editor-preview {
  border-left: 1px solid var(--color-border);
  min-width: 0;
}

.text-editor-actions {
  align-items: center;
  background: var(--color-surface);
  border-top: 1px solid var(--color-border);
  display: flex;
  flex: 0 0 auto;
  gap: 8px;
  justify-content: flex-end;
  padding: 8px 10px;
}

.text-editor-actions .btn {
  align-items: center;
  display: inline-flex;
  gap: 6px;
}

.text-error {
  color: var(--color-danger);
  padding: 14px;
}

@media (max-width: 900px) {
  .text-editor-workspace {
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: minmax(0, 1fr) minmax(0, 1fr);
  }

  .text-editor-preview {
    border-left: 0;
    border-top: 1px solid var(--color-border);
  }
}
</style>
