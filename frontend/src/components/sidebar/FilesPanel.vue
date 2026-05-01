<script setup lang="ts">
import { computed } from "vue";
import { useFilesStore } from "../../stores/files";
import { useLayoutStore } from "../../stores/layout";
import FileTree from "../FileTree.vue";

const emit = defineEmits<{
  "open-file": [path: string];
}>();

const files = useFilesStore();
const layout = useLayoutStore();

const pinned = computed(() => files.pinned);
const currentLabel = computed(() => files.currentPath || "/");

async function openPinned(path: string) {
  try {
    await files.enterDirectory(path);
  } catch {
    emit("open-file", path);
  }
}
</script>

<template>
  <div class="sidebar-panel files-panel">
    <div class="sidebar-section" v-if="pinned.length">
      <div class="section-title">Pinned</div>
      <div
        v-for="path in pinned"
        :key="path"
        class="sidebar-row"
        :class="{ active: layout.openPaths.includes(path) }"
      >
        <button class="sidebar-row-main" type="button" @click="openPinned(path)">
          <i class="bi bi-pin-angle-fill"></i>
          <span class="sidebar-row-name">{{ path || "/" }}</span>
        </button>
        <button class="btn btn-sm icon-button sidebar-row-action" type="button" title="Unpin" @click="files.togglePin(path)">
          <i class="bi bi-x"></i>
        </button>
      </div>
    </div>

    <div class="sidebar-section tree-section">
      <div class="section-title">Files</div>
      <div class="current-path" :title="files.currentPath || '/'">
        <i class="bi bi-folder2-open"></i>
        <span>{{ currentLabel }}</span>
      </div>
      <button v-if="files.currentPath" class="sidebar-row parent-row" type="button" @click="files.enterParentDirectory()">
        <i class="bi bi-arrow-90deg-up"></i>
        <span class="sidebar-row-name">..</span>
      </button>
      <FileTree :entries="files.currentEntries" :active-paths="layout.openPaths" @open-file="emit('open-file', $event)" />
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

.tree-section {
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

.current-path {
  align-items: center;
  color: var(--text-muted);
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

.sidebar-row {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: 6px;
  color: inherit;
  display: flex;
  gap: 7px;
  min-height: 30px;
  padding: 3px 6px;
  text-align: left;
  width: 100%;
}

.sidebar-row:hover,
.sidebar-row.active {
  background: #eef3f8;
}

.parent-row {
  margin-bottom: 2px;
}

.sidebar-row-main {
  align-items: center;
  background: transparent;
  border: 0;
  color: inherit;
  display: flex;
  flex: 1 1 auto;
  gap: 7px;
  min-width: 0;
  padding: 0;
  text-align: left;
}

.sidebar-row-name {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sidebar-row-action {
  flex: 0 0 auto;
  height: 24px;
  opacity: 0.75;
  width: 24px;
}
</style>
