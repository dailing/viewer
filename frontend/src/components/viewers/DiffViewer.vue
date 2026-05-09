<script setup lang="ts">
import hljs from "highlight.js";
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { commitGit, getGitDiff, pushGit, revertGitPath, stageGitPath } from "../../api/client";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { fileChangeAffectsPath } from "../../utils/paths";
import type { WatchEvent } from "../../types/files";

const props = defineProps<{ path: string; paneId: string }>();
const toolbar = usePaneToolbarStore();
const diff = ref("");
const isBinary = ref(false);
const loading = ref(false);
const error = ref("");
const message = ref("");
const container = ref<HTMLElement | null>(null);

const highlightedHtml = computed(() => {
  if (!diff.value) return "";
  return hljs.highlight(diff.value, { language: "diff", ignoreIllegals: true }).value;
});

function setMessage(value: string) {
  message.value = value;
  window.setTimeout(() => {
    if (message.value === value) message.value = "";
  }, 2600);
}

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const result = await getGitDiff(props.path);
    diff.value = result.diff;
    isBinary.value = result.is_binary;
    await nextTick();
    if (container.value) container.value.scrollTop = 0;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    diff.value = "";
  } finally {
    loading.value = false;
    registerToolbar();
  }
}

async function runAction(action: () => Promise<unknown>, success: string) {
  error.value = "";
  try {
    await action();
    setMessage(success);
    await load();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function stageFile() {
  await runAction(() => stageGitPath(props.path), "Staged file");
}

async function stageAll() {
  await runAction(() => stageGitPath(), "Staged all changes");
}

async function revertFile() {
  if (!window.confirm(`Revert changes in ${props.path}?`)) return;
  await runAction(() => revertGitPath(props.path), "Reverted file");
}

async function commitChanges() {
  const commitMessage = window.prompt("Commit message");
  if (!commitMessage) return;
  await runAction(() => commitGit(commitMessage), "Committed changes");
}

async function pushChanges() {
  await runAction(() => pushGit(), "Pushed branch");
}

function registerToolbar() {
  toolbar.setPaneToolbar(props.paneId, {
    title: `Diff: ${props.path}`,
    status: isBinary.value ? "binary" : message.value || undefined,
    statusClass: isBinary.value ? "pane-status-warning" : undefined,
    actions: [
      { id: "refresh-diff", title: "Refresh diff", icon: "bi-arrow-clockwise", run: load },
      { id: "stage-file", title: "Stage file", icon: "bi-plus-square", run: stageFile },
      { id: "stage-all", title: "Stage all changes", label: "All", run: stageAll },
      { id: "revert-file", title: "Revert file", icon: "bi-arrow-counterclockwise", variant: "danger", run: revertFile },
      { id: "commit", title: "Commit staged changes", icon: "bi-check2-square", run: commitChanges },
      { id: "push", title: "Push", icon: "bi-cloud-arrow-up", run: pushChanges },
    ],
  });
}

function handleChange(event: Event) {
  const detail = (event as CustomEvent<WatchEvent>).detail;
  if (fileChangeAffectsPath(detail.path, props.path)) void load();
}

watch(() => props.path, () => void load(), { immediate: true });
watch(message, registerToolbar);

onMounted(() => {
  registerToolbar();
  window.addEventListener("viewer:file-changed", handleChange);
});
onUnmounted(() => {
  window.removeEventListener("viewer:file-changed", handleChange);
  toolbar.clearPaneToolbar(props.paneId);
});
</script>

<template>
  <div class="diff-viewer">
    <div v-if="loading" class="diff-state">
      <div class="spinner-border spinner-border-sm" role="status" aria-label="Loading diff"></div>
    </div>
    <div v-else-if="error" class="diff-state error-state">
      <i class="bi bi-exclamation-triangle"></i>
      <span>{{ error }}</span>
    </div>
    <div v-else-if="isBinary" class="diff-state">
      <i class="bi bi-file-earmark-binary"></i>
      <span>Binary file diff cannot be viewed.</span>
    </div>
    <pre v-else ref="container" class="diff-content hljs markdown-syntax"><code v-html="highlightedHtml"></code></pre>
  </div>
</template>

<style scoped>
.diff-viewer {
  background: var(--syntax-background);
  color: var(--syntax-text);
  height: 100%;
  min-height: 0;
}

.diff-content {
  background: var(--syntax-background);
  color: var(--syntax-text);
  font-size: 12px;
  height: 100%;
  line-height: 1.45;
  margin: 0;
  overflow: auto;
  padding: 12px 14px;
  user-select: text;
  white-space: pre;
}

.diff-state {
  align-items: center;
  color: var(--text-muted);
  display: flex;
  flex-direction: column;
  gap: 8px;
  height: 100%;
  justify-content: center;
  padding: 18px;
  text-align: center;
}

.diff-state .bi {
  font-size: 28px;
}

.error-state {
  color: #a33;
}
</style>
