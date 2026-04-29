<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import ConfigPanel from "./components/ConfigPanel.vue";
import FileSidebar from "./components/FileSidebar.vue";
import Workspace from "./components/Workspace.vue";
import { connectEvents } from "./api/events";
import { useFilesStore } from "./stores/files";
import { useLayoutStore } from "./stores/layout";
import { usePaneToolbarStore } from "./stores/paneToolbar";
import { useTerminalsStore } from "./stores/terminals";
import type { PaneToolbarAction } from "./stores/paneToolbar";
import type { SplitDirection } from "./types/layout";

const SIDEBAR_PIN_KEY = "viewer.sidebarPinned.v1";
const SIDEBAR_WIDTH_KEY = "viewer.sidebarWidth.v1";
const SIDEBAR_MIN_WIDTH = 220;
const SIDEBAR_MAX_WIDTH = 640;
const files = useFilesStore();
const layout = useLayoutStore();
const paneToolbar = usePaneToolbarStore();
const terminals = useTerminalsStore();
const sidebarOpen = ref(false);
const sidebarPinned = ref(false);
const configOpen = ref(false);
const sidebarWidth = ref(320);
const connectionState = ref("connecting");
const bodyShellStyle = computed(() => ({ "--sidebar-width": `${sidebarWidth.value}px` }));
const appStyle = computed(() => {
  const navbarSize = files.appearance.navbar_size;
  const buttonSize = Math.max(18, navbarSize - 4);
  const iconSize = Math.max(11, Math.round(navbarSize * 0.48));
  const theme = files.activeMarkdownTheme;
  return {
    "--topbar-height": `${navbarSize}px`,
    "--nav-button-size": `${buttonSize}px`,
    "--nav-icon-size": `${iconSize}px`,
    "--markdown-body-font-size": `${theme.body.font_size ?? 15}px`,
    "--markdown-body-color": theme.body.color ?? "#172033",
    "--markdown-body-line-height": String(theme.body.line_height ?? 1.65),
    "--markdown-h1-font-size": `${theme.h1.font_size ?? 28}px`,
    "--markdown-h1-color": theme.h1.color ?? "#172033",
    "--markdown-h1-font-weight": theme.h1.font_weight ?? "700",
    "--markdown-h1-line-height": String(theme.h1.line_height ?? 1.2),
    "--markdown-h2-font-size": `${theme.h2.font_size ?? 23}px`,
    "--markdown-h2-color": theme.h2.color ?? "#172033",
    "--markdown-h2-font-weight": theme.h2.font_weight ?? "700",
    "--markdown-h2-line-height": String(theme.h2.line_height ?? 1.25),
    "--markdown-h3-font-size": `${theme.h3.font_size ?? 19}px`,
    "--markdown-h3-color": theme.h3.color ?? "#172033",
    "--markdown-h3-font-weight": theme.h3.font_weight ?? "700",
    "--markdown-h3-line-height": String(theme.h3.line_height ?? 1.3),
    "--markdown-h4-font-size": `${theme.h4.font_size ?? 16}px`,
    "--markdown-h4-color": theme.h4.color ?? "#172033",
    "--markdown-h4-font-weight": theme.h4.font_weight ?? "700",
    "--markdown-h4-line-height": String(theme.h4.line_height ?? 1.35),
    "--markdown-paragraph-font-size": `${theme.paragraph.font_size ?? 15}px`,
    "--markdown-paragraph-color": theme.paragraph.color ?? "#172033",
    "--markdown-paragraph-line-height": String(theme.paragraph.line_height ?? 1.65),
    "--markdown-code-font-size": `${theme.code.font_size ?? 13}px`,
    "--markdown-code-color": theme.code.color ?? "#24292f",
    "--markdown-code-background": theme.code_background,
    "--markdown-link-color": theme.link_color,
    "--markdown-border-color": theme.border_color,
    "--syntax-background": theme.syntax.background,
    "--syntax-text": theme.syntax.text,
    "--syntax-keyword": theme.syntax.keyword,
    "--syntax-string": theme.syntax.string,
    "--syntax-number": theme.syntax.number,
    "--syntax-title": theme.syntax.title,
    "--syntax-comment": theme.syntax.comment,
    "--syntax-meta": theme.syntax.meta,
  };
});
const activePaneToolbar = computed(() => (layout.activePaneId ? paneToolbar.forPane(layout.activePaneId) : undefined));
const activePaneTitle = computed(() => {
  if (activePaneToolbar.value?.title) return activePaneToolbar.value.title;
  const pane = layout.activePane;
  if (!pane || pane.type !== "pane") return "Empty pane";
  if (pane.terminalId) return "Terminal";
  return pane.filePath || "Empty pane";
});
const activePaneActions = computed(() => activePaneToolbar.value?.actions ?? []);
let source: EventSource | null = null;
let terminalRefresh: number | null = null;

function parentPath(path: string): string {
  return path.includes("/") ? path.split("/").slice(0, -1).join("/") : "";
}

function eventAffectsOpenPath(eventPath: string): boolean {
  return layout.openPaths.some((openPath) => openPath === eventPath || parentPath(openPath) === eventPath);
}

onMounted(async () => {
  sidebarPinned.value = localStorage.getItem(SIDEBAR_PIN_KEY) === "true";
  sidebarWidth.value = clampSidebarWidth(Number(localStorage.getItem(SIDEBAR_WIDTH_KEY)) || sidebarWidth.value);
  sidebarOpen.value = sidebarPinned.value;
  layout.load();
  await Promise.all([files.loadConfig(), terminals.load()]);
  await files.loadDirectory(files.currentPath);
  source = connectEvents(
    async (event) => {
      await files.refreshAffected(event.path, event.is_dir);
      if (eventAffectsOpenPath(event.path)) {
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

function runPaneAction(action: PaneToolbarAction) {
  void action.run();
}

onUnmounted(() => {
  source?.close();
  if (terminalRefresh !== null) window.clearInterval(terminalRefresh);
});
</script>

<template>
  <div class="app-shell" :style="appStyle">
    <header class="topbar">
      <button class="btn btn-outline-secondary icon-button" type="button" @click="sidebarOpen = !sidebarOpen" title="Files">
        <i class="bi bi-list"></i>
      </button>
      <button class="btn btn-outline-secondary icon-button" type="button" title="Configuration" @click="configOpen = true">
        <i class="bi bi-gear"></i>
      </button>
      <div class="active-pane-title" :title="activePaneTitle">{{ activePaneTitle }}</div>
      <span v-if="activePaneToolbar?.status" class="pane-status" :class="activePaneToolbar.statusClass">
        {{ activePaneToolbar.status }}
      </span>
      <div v-if="activePaneActions.length" class="pane-actions" aria-label="Active pane actions">
        <button
          v-for="action in activePaneActions"
          :key="action.id"
          class="btn btn-outline-secondary icon-button toolbar-action"
          :class="[{ active: action.active, 'has-label': action.label }, action.variant === 'danger' ? 'toolbar-action-danger' : '']"
          type="button"
          :title="action.title"
          :aria-label="action.title"
          @click="runPaneAction(action)"
        >
          <i v-if="action.icon" class="bi" :class="action.icon"></i>
          <span v-else-if="action.label">{{ action.label }}</span>
        </button>
      </div>
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
    <ConfigPanel v-if="configOpen" @close="configOpen = false" />
  </div>
</template>
