<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import FileSidebar from "./components/FileSidebar.vue";
import Workspace from "./components/Workspace.vue";
import { connectEvents } from "./api/events";
import { useFilesStore } from "./stores/files";
import { useLayoutStore } from "./stores/layout";
import { useTerminalsStore } from "./stores/terminals";
import type { SplitDirection } from "./types/layout";

const SIDEBAR_PIN_KEY = "viewer.sidebarPinned.v1";
const SIDEBAR_WIDTH_KEY = "viewer.sidebarWidth.v1";
const SIDEBAR_MIN_WIDTH = 220;
const SIDEBAR_MAX_WIDTH = 640;
const files = useFilesStore();
const layout = useLayoutStore();
const terminals = useTerminalsStore();
const sidebarOpen = ref(false);
const sidebarPinned = ref(false);
const sidebarWidth = ref(320);
const connectionState = ref("connecting");
const bodyShellStyle = computed(() => ({ "--sidebar-width": `${sidebarWidth.value}px` }));
const activePaneTitle = computed(() => {
  const pane = layout.activePane;
  if (!pane || pane.type !== "pane") return "Empty pane";
  if (pane.terminalId) return "Terminal";
  return pane.filePath || "Empty pane";
});
let source: EventSource | null = null;
let terminalRefresh: number | null = null;

onMounted(async () => {
  sidebarPinned.value = localStorage.getItem(SIDEBAR_PIN_KEY) === "true";
  sidebarWidth.value = clampSidebarWidth(Number(localStorage.getItem(SIDEBAR_WIDTH_KEY)) || sidebarWidth.value);
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

function clampSidebarWidth(width: number) {
  return Math.min(SIDEBAR_MAX_WIDTH, Math.max(SIDEBAR_MIN_WIDTH, width));
}

function startSidebarResize(event: PointerEvent) {
  if (!sidebarPinned.value) return;

  event.preventDefault();
  const handle = event.currentTarget as HTMLElement;
  handle.setPointerCapture(event.pointerId);
  document.body.classList.add("sidebar-resizing");

  const resize = (moveEvent: PointerEvent) => {
    const nextWidth = clampSidebarWidth(moveEvent.clientX);
    sidebarWidth.value = nextWidth;
    localStorage.setItem(SIDEBAR_WIDTH_KEY, String(nextWidth));
  };

  const stop = () => {
    document.body.classList.remove("sidebar-resizing");
    window.removeEventListener("pointermove", resize);
    window.removeEventListener("pointerup", stop);
    window.removeEventListener("pointercancel", stop);
  };

  window.addEventListener("pointermove", resize);
  window.addEventListener("pointerup", stop);
  window.addEventListener("pointercancel", stop);
}

function splitActivePane(direction: SplitDirection) {
  if (layout.activePaneId) layout.splitPane(layout.activePaneId, direction);
}

function closeActivePane() {
  if (layout.activePaneId) layout.closePane(layout.activePaneId);
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
      <div class="active-pane-title" :title="activePaneTitle">{{ activePaneTitle }}</div>
      <button class="btn btn-outline-secondary icon-button" type="button" title="Split vertical" @click="splitActivePane('vertical')">
        <i class="bi bi-layout-split"></i>
      </button>
      <button class="btn btn-outline-secondary icon-button" type="button" title="Split horizontal" @click="splitActivePane('horizontal')">
        <i class="bi bi-distribute-vertical"></i>
      </button>
      <button class="btn btn-outline-secondary icon-button" type="button" title="Close pane" @click="closeActivePane">
        <i class="bi bi-x"></i>
      </button>
      <span class="status-dot" :class="connectionState"></span>
    </header>

    <div class="body-shell" :class="{ 'sidebar-pinned': sidebarPinned }" :style="bodyShellStyle">
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
      <div
        v-if="sidebarPinned"
        class="sidebar-resizer"
        role="separator"
        title="Drag to resize"
        @pointerdown="startSidebarResize"
      ></div>
      <main class="workspace-wrap">
        <Workspace />
      </main>
    </div>
  </div>
</template>
