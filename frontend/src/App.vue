<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from "vue";
import FileSidebar from "./components/FileSidebar.vue";
import Workspace from "./components/Workspace.vue";
import { connectEvents } from "./api/events";
import { useFilesStore } from "./stores/files";
import { useLayoutStore } from "./stores/layout";
import { useTerminalsStore } from "./stores/terminals";

const SIDEBAR_PIN_KEY = "viewer.sidebarPinned.v1";
const files = useFilesStore();
const layout = useLayoutStore();
const terminals = useTerminalsStore();
const sidebarOpen = ref(false);
const sidebarPinned = ref(false);
const connectionState = ref("connecting");
let source: EventSource | null = null;
let terminalRefresh: number | null = null;

onMounted(async () => {
  sidebarPinned.value = localStorage.getItem(SIDEBAR_PIN_KEY) === "true";
  sidebarOpen.value = sidebarPinned.value;
  layout.load();
  await Promise.all([files.loadDirectory(""), files.loadConfig(), terminals.load()]);
  source = connectEvents(
    async (event) => {
      await files.refreshAffected(event.path, event.is_dir);
      if (layout.openPaths.includes(event.path)) {
        window.dispatchEvent(new CustomEvent("viewer:file-changed", { detail: event }));
      }
    },
    (state) => (connectionState.value = state),
  );
  terminalRefresh = window.setInterval(() => {
    void terminals.load();
  }, 3000);
});

watch(sidebarPinned, (pinned) => {
  localStorage.setItem(SIDEBAR_PIN_KEY, String(pinned));
  if (pinned) sidebarOpen.value = true;
});

function openFile(path: string) {
  layout.openFile(path);
  if (!sidebarPinned.value) sidebarOpen.value = false;
}

function openTerminal(id: string) {
  layout.openTerminal(id);
  if (!sidebarPinned.value) sidebarOpen.value = false;
}

function toggleSidebarPin() {
  sidebarPinned.value = !sidebarPinned.value;
}

onUnmounted(() => {
  source?.close();
  if (terminalRefresh !== null) window.clearInterval(terminalRefresh);
});
</script>

<template>
  <div class="app-shell">
    <header class="topbar">
      <button class="btn btn-outline-secondary icon-button" type="button" @click="sidebarOpen = !sidebarOpen" title="Files">
        <i class="bi bi-list"></i>
      </button>
      <div class="brand">Viewer</div>
      <span class="status-dot" :class="connectionState"></span>
      <span class="status-text">{{ connectionState }}</span>
    </header>

    <div class="body-shell" :class="{ 'sidebar-pinned': sidebarPinned }">
      <div v-if="sidebarOpen && !sidebarPinned" class="sidebar-backdrop" @click="sidebarOpen = false"></div>
      <aside class="sidebar-drawer" :class="{ open: sidebarOpen, pinned: sidebarPinned }">
        <div class="sidebar-chrome">
          <span>Files</span>
          <button
            class="btn btn-sm btn-outline-secondary icon-button"
            type="button"
            :title="sidebarPinned ? 'Unpin panel' : 'Pin panel'"
            @click="toggleSidebarPin"
          >
            <i class="bi" :class="sidebarPinned ? 'bi-pin-angle-fill' : 'bi-pin-angle'"></i>
          </button>
          <button
            v-if="!sidebarPinned"
            class="btn btn-sm btn-outline-secondary icon-button"
            type="button"
            title="Hide panel"
            @click="sidebarOpen = false"
          >
            <i class="bi bi-x"></i>
          </button>
        </div>
        <FileSidebar @open-file="openFile" @open-terminal="openTerminal" />
      </aside>
      <main class="workspace-wrap">
        <Workspace />
      </main>
    </div>
  </div>
</template>
