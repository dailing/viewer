<script setup lang="ts">
import type { FileEntry } from "../types/files";
import { useFilesStore } from "../stores/files";

const props = defineProps<{ entries: FileEntry[]; activePaths?: string[] }>();
const emit = defineEmits<{ "open-file": [path: string] }>();
const files = useFilesStore();

function icon(entry: FileEntry): string {
  if (entry.is_dir) return "bi-folder";
  if (entry.mime?.startsWith("image/")) return "bi-file-earmark-image";
  if (entry.name.toLowerCase().endsWith(".md")) return "bi-markdown";
  if (entry.name.toLowerCase().endsWith(".pdf")) return "bi-file-earmark-pdf";
  return "bi-file-earmark";
}

async function select(entry: FileEntry) {
  if (entry.is_dir) {
    await files.enterDirectory(entry.path);
    return;
  }
  emit("open-file", entry.path);
}

function isActive(entry: FileEntry): boolean {
  return !entry.is_dir && (props.activePaths ?? []).includes(entry.path);
}
</script>

<template>
  <div class="tree-list">
    <div v-for="entry in entries" :key="entry.path">
      <div class="tree-row" :class="{ active: isActive(entry) }">
        <button class="tree-main" type="button" @click="select(entry)" :title="entry.path">
          <i class="bi" :class="icon(entry)"></i>
          <span class="entry-name">{{ entry.name }}</span>
          <i v-if="entry.is_symlink" class="bi bi-link-45deg muted"></i>
        </button>
        <button class="btn btn-sm icon-button pin-button" type="button" title="Pin" @click="files.togglePin(entry.path)">
          <i class="bi" :class="files.pinned.includes(entry.path) ? 'bi-pin-angle-fill' : 'bi-pin-angle'"></i>
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tree-list {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.tree-row {
  align-items: center;
  border-radius: 6px;
  display: flex;
  min-height: 30px;
}

.tree-row:hover,
.tree-row.active {
  background: #eef3f8;
}

.tree-row.active {
  color: #0b5ed7;
  font-weight: 600;
}

.tree-main {
  align-items: center;
  background: transparent;
  border: 0;
  display: flex;
  flex: 1 1 auto;
  gap: 7px;
  min-width: 0;
  padding: 3px 4px;
  text-align: left;
}

.entry-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pin-button {
  flex: 0 0 auto;
  height: 24px;
  opacity: 0.72;
  width: 24px;
}
</style>
