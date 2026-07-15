<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { getGitStatus } from "../../api/client";
import { useLayoutStore } from "../../stores/layout";
import type { GitDiffFile } from "../../types/git";

const emit = defineEmits<{
  "open-diff": [path: string, cwd: string];
}>();

const props = defineProps<{
  defaultCwd?: string;
}>();

const layout = useLayoutStore();
const files = ref<GitDiffFile[]>([]);
const loading = ref(false);
const loaded = ref(false);
const error = ref("");
const currentCwd = computed(() => props.defaultCwd ?? "");

watch(
  currentCwd,
  () => {
    files.value = [];
    loaded.value = false;
    error.value = "";
    void load();
  },
  { immediate: true },
);

async function load() {
  if (loading.value) return;
  loading.value = true;
  error.value = "";
  try {
    files.value = (await getGitStatus(currentCwd.value)).files;
    loaded.value = true;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    files.value = [];
  } finally {
    loading.value = false;
  }
}

function open(file: GitDiffFile) {
  if (file.is_binary) return;
  emit("open-diff", file.path, currentCwd.value);
}

function statusTitle(file: GitDiffFile) {
  if (file.is_binary) return "Binary file cannot be viewed";
  return `Open diff for ${file.path}`;
}

</script>

<template>
  <div class="sidebar-panel">
    <div class="sidebar-section">
      <button class="btn btn-sm btn-outline-secondary panel-command" type="button" title="Refresh changes" @click="load">
        <i class="bi bi-arrow-clockwise"></i>
        <span>Refresh</span>
      </button>
    </div>

    <div class="sidebar-section list-section">
      <div class="section-title">Changes</div>
      <div class="current-path" :title="currentCwd || '/'">
        <i class="bi bi-folder2-open"></i>
        <span>{{ currentCwd || "/" }}</span>
      </div>
      <div v-if="error" class="panel-error">{{ error }}</div>
      <div v-else-if="loading && !files.length" class="empty-panel">Loading changes</div>
      <div v-else-if="!loaded" class="empty-panel">Use Refresh to load changes</div>
      <div v-else-if="!files.length" class="empty-panel">No changes</div>
      <button
        v-for="file in files"
        :key="`${file.status}:${file.path}`"
        class="change-row"
        :class="{ active: layout.openDiffPaths.includes(file.path), disabled: file.is_binary }"
        type="button"
        :title="statusTitle(file)"
        :disabled="file.is_binary"
        @click="open(file)"
      >
        <span class="status-code">{{ file.status.trim() || "M" }}</span>
        <span class="change-path">{{ file.path }}</span>
        <span v-if="file.is_binary" class="binary-pill">bin</span>
        <span v-else class="line-stats">
          <span class="added">+{{ file.added ?? 0 }}</span>
          <span class="deleted">-{{ file.deleted ?? 0 }}</span>
        </span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.sidebar-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.sidebar-section {
  border-bottom: 1px solid var(--color-border);
  padding: 10px;
}

.list-section {
  border-bottom: 0;
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
}

.section-title {
  color: var(--color-text-muted);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0;
  margin-bottom: 6px;
  text-transform: uppercase;
}

.current-path {
  align-items: center;
  color: var(--color-text-muted);
  display: flex;
  font-size: 12px;
  gap: 7px;
  margin-bottom: 6px;
  min-width: 0;
  padding: 2px 6px;
}

.current-path span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.panel-command {
  align-items: center;
  display: inline-flex;
  gap: 7px;
  justify-content: center;
  width: 100%;
}

.empty-panel,
.panel-error {
  color: var(--color-text-muted);
  font-size: 12px;
  padding: 4px 6px;
}

.panel-error {
  color: var(--color-danger);
}

.change-row {
  align-items: center;
  background: transparent;
  border: 1px solid transparent;
  border-radius: 6px;
  color: inherit;
  display: grid;
  font-size: 11px;
  gap: 6px;
  grid-template-columns: 28px minmax(0, 1fr) auto;
  min-height: 28px;
  padding: 3px 6px;
  text-align: left;
  width: 100%;
}

.change-row:hover {
  background: var(--color-surface-hover);
}

.change-row.active {
  border-color: var(--color-accent);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--color-accent) 18%, transparent);
}

.change-row.disabled {
  color: var(--color-text-muted);
  cursor: not-allowed;
}

.status-code {
  color: var(--color-text-muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 10px;
}

.change-path {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.line-stats {
  display: inline-flex;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 10px;
  gap: 5px;
}

.added {
  color: var(--color-success);
}

.deleted {
  color: var(--color-danger);
}

.binary-pill {
  border: 1px solid var(--color-border);
  border-radius: 999px;
  color: var(--color-text-muted);
  font-size: 10px;
  padding: 1px 5px;
}
</style>
