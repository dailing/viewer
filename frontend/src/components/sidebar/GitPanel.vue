<script setup lang="ts">
import { ref } from "vue";
import { getGitStatus } from "../../api/client";
import { useFilesStore } from "../../stores/files";
import { useLayoutStore } from "../../stores/layout";
import type { GitDiffFile } from "../../types/git";

const emit = defineEmits<{
  "open-diff": [path: string, cwd: string];
}>();

const layout = useLayoutStore();
const fileStore = useFilesStore();
const files = ref<GitDiffFile[]>([]);
const loading = ref(false);
const loaded = ref(false);
const error = ref("");

async function load() {
  if (loading.value) return;
  loading.value = true;
  error.value = "";
  try {
    files.value = (await getGitStatus(fileStore.currentPath)).files;
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
  emit("open-diff", file.path, fileStore.currentPath);
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
  border-bottom: 1px solid var(--border);
  padding: 10px;
}

.list-section {
  border-bottom: 0;
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
}

.section-title {
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0;
  margin-bottom: 6px;
  text-transform: uppercase;
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
  color: var(--text-muted);
  font-size: 12px;
  padding: 4px 6px;
}

.panel-error {
  color: #a33;
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
  background: #eef3f8;
}

.change-row.active {
  border-color: #2f6fdd;
  box-shadow: inset 0 0 0 1px rgb(47 111 221 / 0.18);
}

.change-row.disabled {
  color: var(--text-muted);
  cursor: not-allowed;
}

.status-code {
  color: #5f6f86;
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
  color: #116329;
}

.deleted {
  color: #a40e26;
}

.binary-pill {
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--text-muted);
  font-size: 10px;
  padding: 1px 5px;
}
</style>
