<script setup lang="ts">
import { computed } from "vue";
import { useFilesStore } from "../stores/files";
import { useLayoutStore } from "../stores/layout";
import { useTerminalsStore } from "../stores/terminals";
import FileTree from "./FileTree.vue";

const emit = defineEmits<{ "open-file": [path: string]; "open-terminal": [id: string] }>();
const files = useFilesStore();
const layout = useLayoutStore();
const terminals = useTerminalsStore();

const pinned = computed(() => files.pinned);
const currentLabel = computed(() => files.currentPath || "/");

async function openPinned(path: string) {
  try {
    await files.enterDirectory(path);
  } catch {
    emit("open-file", path);
  }
}

async function newTerminal() {
  const terminal = await terminals.create(files.currentPath);
  emit("open-terminal", terminal.id);
}

async function closeTerminal(id: string) {
  await terminals.remove(id);
  layout.clearTerminal(id);
}
</script>

<template>
  <div class="file-sidebar">
    <div class="sidebar-section">
      <button class="btn btn-sm btn-primary terminal-new" type="button" @click="newTerminal">
        <i class="bi bi-terminal"></i>
        <span>New Terminal</span>
      </button>
    </div>

    <div class="sidebar-section" v-if="terminals.terminals.length">
      <div class="section-title">Terminals</div>
      <div
        v-for="terminal in terminals.terminals"
        :key="terminal.id"
        class="file-row pinned-row"
        :class="{ active: layout.openTerminalIds.includes(terminal.id) }"
      >
        <button class="file-main" type="button" @click="emit('open-terminal', terminal.id)">
          <i class="bi" :class="terminal.status === 'running' ? 'bi-terminal-fill' : 'bi-terminal'"></i>
          <span class="file-name">{{ terminal.title }}</span>
        </button>
        <span class="terminal-state" :class="terminal.status">{{ terminal.status }}</span>
        <button class="btn btn-sm icon-button pin-action" type="button" title="Close terminal" @click="closeTerminal(terminal.id)">
          <i class="bi bi-x"></i>
        </button>
      </div>
    </div>

    <div class="sidebar-section" v-if="pinned.length">
      <div class="section-title">Pinned</div>
      <div
        v-for="path in pinned"
        :key="path"
        class="file-row pinned-row"
        :class="{ active: layout.openPaths.includes(path) }"
      >
        <button class="file-main" type="button" @click="openPinned(path)">
          <i class="bi bi-pin-angle-fill"></i>
          <span class="file-name">{{ path || "/" }}</span>
        </button>
        <button class="btn btn-sm icon-button pin-action" type="button" title="Unpin" @click="files.togglePin(path)">
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
      <button v-if="files.currentPath" class="file-row parent-row" type="button" @click="files.enterParentDirectory()">
        <i class="bi bi-arrow-90deg-up"></i>
        <span class="file-name">..</span>
      </button>
      <FileTree :entries="files.currentEntries" :active-paths="layout.openPaths" @open-file="emit('open-file', $event)" />
    </div>
  </div>
</template>

<style scoped>
.file-sidebar {
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

.file-row {
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

.file-row:hover,
.file-row.active {
  background: #eef3f8;
}

.parent-row {
  margin-bottom: 2px;
}

.file-main {
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

.file-name {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pin-action {
  flex: 0 0 auto;
  height: 24px;
  opacity: 0.75;
  width: 24px;
}

.terminal-new {
  align-items: center;
  display: inline-flex;
  gap: 7px;
  justify-content: center;
  width: 100%;
}

.terminal-state {
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--text-muted);
  flex: 0 0 auto;
  font-size: 10px;
  line-height: 1;
  padding: 3px 6px;
}

.terminal-state.running {
  border-color: #9fc5a8;
  color: #146c43;
}
</style>
