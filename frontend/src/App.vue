<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import ConfigPanel from "./components/ConfigPanel.vue";
import FileSidebar from "./components/FileSidebar.vue";
import LoopTasksPage from "./components/LoopTasksPage.vue";
import Workspace from "./components/Workspace.vue";
import { connectEvents } from "./api/events";
import { useCodexStore } from "./stores/codex";
import { useFilesStore } from "./stores/files";
import { useLayoutStore } from "./stores/layout";
import { usePaneToolbarStore } from "./stores/paneToolbar";
import { useTerminalsStore } from "./stores/terminals";
import { useWorkspacesStore } from "./stores/workspaces";
import { parentPath } from "./utils/paths";
import type { PaneToolbarAction, PaneToolbarControl } from "./stores/paneToolbar";
import type { CodexSessionInfo, CodexStatus } from "./types/codex";
import type { LayoutNode, SplitDirection } from "./types/layout";

const SIDEBAR_PIN_KEY = "viewer.sidebarPinned.v1";
const SIDEBAR_WIDTH_KEY = "viewer.sidebarWidth.v1";
const SIDEBAR_MIN_WIDTH = 220;
const SIDEBAR_MAX_WIDTH = 640;
type WorkspaceAlert = "completed" | "failed";
type WorkspaceNotice = WorkspaceAlert | "running";
const files = useFilesStore();
const codex = useCodexStore();
const layout = useLayoutStore();
const paneToolbar = usePaneToolbarStore();
const terminals = useTerminalsStore();
const workspaces = useWorkspacesStore();
const sidebarOpen = ref(false);
const sidebarPinned = ref(false);
const activePage = ref<"workspace" | "settings" | "loops">("workspace");
const mobileToolbarOpen = ref(false);
const codexStatusById = ref<Record<string, CodexStatus>>({});
const workspaceAlerts = ref<Record<string, WorkspaceAlert>>({});
const sidebarWidth = ref(320);
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
  if (pane.codexSessionId) return codex.sessions.find((session) => session.id === pane.codexSessionId)?.title ?? "Codex";
  return pane.filePath || "Empty pane";
});
const activePaneHasContent = computed(() => {
  const pane = layout.activePane;
  return Boolean(pane?.type === "pane" && (pane.filePath || pane.terminalId || pane.codexSessionId));
});
const globalPaneActions = computed<PaneToolbarAction[]>(() => {
  if (!layout.activePaneId) return [];
  return [
    {
      id: "split-vertical",
      title: "Split pane right",
      icon: "bi-layout-split",
      run: () => splitActivePane("vertical"),
    },
    {
      id: "split-horizontal",
      title: "Split pane down",
      icon: "bi-view-stacked",
      run: () => splitActivePane("horizontal"),
    },
    {
      id: "close-pane",
      title: activePaneHasContent.value ? "Clear pane content" : "Close empty pane",
      icon: "bi-x-lg",
      run: () => closeActivePane(),
    },
  ];
});
const activePaneActions = computed(() => activePaneToolbar.value?.actions ?? []);
const activePaneControls = computed(() => activePaneToolbar.value?.controls ?? []);
const hasMobilePaneToolbar = computed(() => activePaneActions.value.length > 0 || activePaneControls.value.length > 0);
const workspaceCount = computed(() => files.workspaceConfig.count);
const workspaceNotices = computed<Record<string, WorkspaceNotice>>(() => {
  const notices: Record<string, WorkspaceNotice> = { ...workspaceAlerts.value };
  for (const session of codex.sessions) {
    if (session.status !== "running") continue;
    for (const workspaceId of workspaceIdsForCodexSession(session.id)) {
      if (!notices[workspaceId]) notices[workspaceId] = "running";
    }
  }
  return notices;
});
let source: EventSource | null = null;
let terminalRefresh: number | null = null;
let codexRefresh: number | null = null;
let workspaceSaveTimer: number | null = null;
let workspaceAutosaveReady = false;

function eventAffectsOpenPath(eventPath: string): boolean {
  return layout.openPaths.some((openPath) => openPath === eventPath || parentPath(openPath) === eventPath);
}

onMounted(async () => {
  sidebarPinned.value = localStorage.getItem(SIDEBAR_PIN_KEY) === "true";
  sidebarWidth.value = clampSidebarWidth(Number(localStorage.getItem(SIDEBAR_WIDTH_KEY)) || sidebarWidth.value);
  sidebarOpen.value = sidebarPinned.value;
  layout.load();
  await Promise.all([files.loadConfig(), terminals.load(), codex.load(), workspaces.load()]);
  await restoreInitialWorkspace();
  updateWorkspaceCodexNotices();
  workspaceAutosaveReady = true;
  source = connectEvents(
    async (event) => {
      await files.refreshAffected(event.path, event.is_dir);
      if (eventAffectsOpenPath(event.path)) {
        window.dispatchEvent(new CustomEvent("viewer:file-changed", { detail: event }));
      }
    },
  );
  terminalRefresh = window.setInterval(() => {
    void terminals.load();
  }, 3000);
  codexRefresh = window.setInterval(() => {
    void codex.load();
  }, 3000);
});

watch(
  [() => layout.root, () => layout.activePaneId, () => files.currentPath, () => files.pinned, () => workspaces.activeCodexSessionIds],
  () => {
    scheduleWorkspaceSave();
  },
  { deep: true },
);

watch(workspaceCount, () => {
  if (!workspaceAutosaveReady) return;
  const activeId = normalizeWorkspaceId(workspaces.activeWorkspaceId);
  if (activeId !== workspaces.activeWorkspaceId) {
    void switchWorkspace(activeId);
  }
});

watch(sidebarPinned, (pinned) => {
  localStorage.setItem(SIDEBAR_PIN_KEY, String(pinned));
  if (pinned) sidebarOpen.value = true;
});

watch(
  () => codex.sessions.map((session) => `${session.id}:${session.status}`).join("|"),
  () => {
    updateWorkspaceCodexNotices();
  },
);

function openFile(path: string) {
  void files.recordVisit(path);
  layout.openFile(path);
  if (!sidebarPinned.value) sidebarOpen.value = false;
}

function openTerminal(id: string) {
  layout.openTerminal(id);
  if (!sidebarPinned.value) sidebarOpen.value = false;
}

function openCodexSession(id: string) {
  workspaces.rememberActiveCodexSession(id);
  layout.openCodexSession(id);
  if (!sidebarPinned.value) sidebarOpen.value = false;
}

function currentWorkspaceSnapshot() {
  const snapshot = layout.snapshot();
  return {
    layout: snapshot.root,
    active_pane_id: snapshot.activePaneId,
    current_path: files.currentPath,
    pinned: [...files.pinned],
    codex_session_ids: [...workspaces.activeCodexSessionIds],
  };
}

function scheduleWorkspaceSave() {
  if (!workspaceAutosaveReady || workspaces.switching) return;
  if (workspaceSaveTimer !== null) window.clearTimeout(workspaceSaveTimer);
  workspaceSaveTimer = window.setTimeout(() => {
    workspaceSaveTimer = null;
    void workspaces.saveSlot(workspaces.activeWorkspaceId, currentWorkspaceSnapshot());
  }, 500);
}

function normalizeWorkspaceId(id: string) {
  const index = Number(id);
  if (!Number.isInteger(index) || index < 1 || index > workspaceCount.value) return "1";
  return String(index);
}

async function loadWorkspaceDirectory(path: string) {
  try {
    await files.enterDirectory(path);
  } catch {
    await files.enterDirectory("");
  }
}

function restoreWorkspacePins(pinned?: string[] | null) {
  files.pinned = [...(pinned ?? files.pinned)];
}

async function restoreInitialWorkspace() {
  const activeId = normalizeWorkspaceId(workspaces.activeWorkspaceId);
  if (activeId !== workspaces.activeWorkspaceId) {
    await workspaces.activate(activeId);
  }
  const snapshot = workspaces.snapshotFor(activeId);
  if (snapshot) {
    layout.restore(snapshot.layout, snapshot.active_pane_id);
    workspaces.restoreActiveCodexSessions(snapshot);
    restoreWorkspacePins(snapshot.pinned);
    await loadWorkspaceDirectory(snapshot.current_path);
    clearWorkspaceNotice(activeId);
    return;
  }
  await files.loadDirectory(files.currentPath);
  workspaces.restoreActiveCodexSessions(currentWorkspaceSnapshot());
  await workspaces.saveSlot(activeId, currentWorkspaceSnapshot());
  clearWorkspaceNotice(activeId);
}

async function switchWorkspace(id: string) {
  const targetId = normalizeWorkspaceId(id);
  if (workspaces.switching || targetId === workspaces.activeWorkspaceId) return;
  workspaces.switching = true;
  if (workspaceSaveTimer !== null) {
    window.clearTimeout(workspaceSaveTimer);
    workspaceSaveTimer = null;
  }
  try {
    await workspaces.saveSlot(workspaces.activeWorkspaceId, currentWorkspaceSnapshot());
    const target = workspaces.snapshotFor(targetId);
    if (target) {
      layout.restore(target.layout, target.active_pane_id);
      workspaces.restoreActiveCodexSessions(target);
      restoreWorkspacePins(target.pinned);
      await loadWorkspaceDirectory(target.current_path);
      await workspaces.activate(targetId);
    } else {
      layout.reset();
      workspaces.restoreActiveCodexSessions(null);
      files.pinned = [];
      await workspaces.saveSlot(targetId, currentWorkspaceSnapshot());
    }
    clearWorkspaceNotice(targetId);
  } finally {
    workspaces.switching = false;
  }
}

function updateWorkspaceCodexNotices() {
  const previous = codexStatusById.value;
  const next: Record<string, CodexStatus> = {};

  for (const session of codex.sessions) {
    next[session.id] = session.status;
    if (!["exited", "failed"].includes(session.status)) continue;
    const notice: WorkspaceAlert = session.status === "failed" ? "failed" : "completed";
    for (const workspaceId of workspaceIdsForCodexSession(session.id)) {
      if (workspaceId === workspaces.activeWorkspaceId) continue;
      if (previous[session.id] !== "running" && !sessionFinishedAfterWorkspaceSaved(session, workspaceId)) continue;
      setWorkspaceNotice(workspaceId, notice);
    }
  }

  codexStatusById.value = next;
}

function workspaceIdsForCodexSession(sessionId: string) {
  const ids: string[] = [];
  for (let index = 1; index <= workspaceCount.value; index += 1) {
    const workspaceId = String(index);
    const root = workspaceId === workspaces.activeWorkspaceId ? layout.root : workspaces.snapshotFor(workspaceId)?.layout;
    if (root && layoutContainsCodexSession(root, sessionId)) ids.push(workspaceId);
  }
  return ids;
}

function layoutContainsCodexSession(node: LayoutNode, sessionId: string): boolean {
  if (node.type === "pane") return node.codexSessionId === sessionId;
  return layoutContainsCodexSession(node.first, sessionId) || layoutContainsCodexSession(node.second, sessionId);
}

function sessionFinishedAfterWorkspaceSaved(session: CodexSessionInfo, workspaceId: string) {
  const snapshotUpdatedAt = workspaces.snapshotFor(workspaceId)?.updated_at;
  return Boolean(snapshotUpdatedAt && session.updated_at > snapshotUpdatedAt);
}

function setWorkspaceNotice(workspaceId: string, notice: WorkspaceAlert) {
  const current = workspaceAlerts.value[workspaceId];
  workspaceAlerts.value = {
    ...workspaceAlerts.value,
    [workspaceId]: current === "failed" ? current : notice,
  };
}

function clearWorkspaceNotice(workspaceId: string) {
  if (!workspaceAlerts.value[workspaceId]) return;
  const next = { ...workspaceAlerts.value };
  delete next[workspaceId];
  workspaceAlerts.value = next;
}

function toggleSidebarPin() {
  sidebarPinned.value = !sidebarPinned.value;
}

function toggleToolPanel() {
  sidebarOpen.value = !sidebarOpen.value;
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

function runPaneAction(action: PaneToolbarAction) {
  void action.run();
}

function runMobilePaneAction(action: PaneToolbarAction) {
  mobileToolbarOpen.value = false;
  runPaneAction(action);
}

function updatePaneControl(control: PaneToolbarControl, event: Event) {
  if (control.kind !== "select") return;
  const target = event.target as HTMLSelectElement | null;
  if (!target) return;
  void control.onChange(target.value);
}

function updateMobilePaneControl(control: PaneToolbarControl, event: Event) {
  updatePaneControl(control, event);
  mobileToolbarOpen.value = false;
}

function splitActivePane(direction: SplitDirection) {
  if (!layout.activePaneId) return;
  layout.splitPane(layout.activePaneId, direction);
}

function closeActivePane() {
  const paneId = layout.activePaneId;
  if (!paneId) return;
  if (activePaneHasContent.value) {
    layout.clearPane(paneId);
    return;
  }
  layout.closePane(paneId);
}

onUnmounted(() => {
  source?.close();
  if (workspaceSaveTimer !== null) window.clearTimeout(workspaceSaveTimer);
  if (terminalRefresh !== null) window.clearInterval(terminalRefresh);
  if (codexRefresh !== null) window.clearInterval(codexRefresh);
});
</script>

<template>
  <div class="app-shell" :style="appStyle">
    <header class="topbar">
      <button
        class="btn btn-outline-secondary icon-button"
        :class="{ active: activePage === 'settings' }"
        type="button"
        title="Settings"
        @click="activePage = activePage === 'settings' ? 'workspace' : 'settings'"
      >
        <i class="bi bi-gear"></i>
      </button>
      <button
        class="btn btn-outline-secondary icon-button"
        :class="{ active: activePage === 'loops' }"
        type="button"
        title="Loop Tasks"
        @click="activePage = activePage === 'loops' ? 'workspace' : 'loops'"
      >
        <i class="bi bi-clock-history"></i>
      </button>
      <div class="active-pane-title" :title="activePage === 'workspace' ? activePaneTitle : activePage === 'settings' ? 'Settings' : 'Loop Tasks'">
        {{ activePage === 'workspace' ? activePaneTitle : activePage === 'settings' ? 'Settings' : 'Loop Tasks' }}
      </div>
      <span v-if="activePage === 'workspace' && activePaneToolbar?.status" class="pane-status" :class="activePaneToolbar.statusClass">
        {{ activePaneToolbar.status }}
      </span>
      <div v-if="activePage === 'workspace' && hasMobilePaneToolbar" class="mobile-pane-menu">
        <button
          class="btn btn-outline-secondary icon-button toolbar-action"
          type="button"
          title="Pane controls"
          aria-label="Pane controls"
          :aria-expanded="mobileToolbarOpen"
          @click="mobileToolbarOpen = !mobileToolbarOpen"
        >
          <i class="bi bi-three-dots-vertical"></i>
        </button>
        <div v-if="mobileToolbarOpen" class="mobile-pane-menu-panel" role="menu">
          <button
            v-for="action in activePaneActions"
            :key="action.id"
            class="mobile-pane-menu-item"
            :class="[{ active: action.active }, action.variant === 'danger' ? 'danger' : '']"
            type="button"
            role="menuitem"
            @click="runMobilePaneAction(action)"
          >
            <i v-if="action.icon" class="bi" :class="action.icon"></i>
            <span v-else-if="action.label" class="mobile-pane-menu-label">{{ action.label }}</span>
            <span>{{ action.title }}</span>
          </button>
          <template v-for="control in activePaneControls" :key="control.id">
            <label v-if="control.kind === 'select'" class="mobile-pane-menu-control">
              <span>{{ control.title }}</span>
              <select
                class="form-select form-select-sm"
                :value="control.value"
                @change="updateMobilePaneControl(control, $event)"
              >
                <option v-for="option in control.options" :key="option" :value="option">{{ option }}</option>
              </select>
            </label>
            <div v-else class="mobile-pane-menu-control">
              <span>{{ control.title }}</span>
              <div class="mobile-pane-menu-chips">
                <span v-for="(item, index) in control.items" :key="`${index}:${item}`" class="pane-toolbar-chip">{{ item }}</span>
              </div>
            </div>
          </template>
        </div>
      </div>
      <div v-if="activePage === 'workspace' && activePaneActions.length" class="pane-actions" aria-label="Active pane actions">
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
      <template v-if="activePage === 'workspace'" v-for="control in activePaneControls" :key="control.id">
        <select
          v-if="control.kind === 'select'"
          class="form-select form-select-sm pane-toolbar-select"
          :class="{ 'pane-toolbar-select-compact': control.size === 'compact' }"
          :title="control.title"
          :value="control.value"
          @change="updatePaneControl(control, $event)"
        >
          <option v-for="option in control.options" :key="option" :value="option">{{ option }}</option>
        </select>
        <div v-else class="pane-toolbar-chips" :title="control.title">
          <span v-for="(item, index) in control.items" :key="`${index}:${item}`" class="pane-toolbar-chip">{{ item }}</span>
        </div>
      </template>
      <div v-if="activePage === 'workspace' && globalPaneActions.length" class="pane-actions global-pane-actions" aria-label="Global pane actions">
        <button
          v-for="action in globalPaneActions"
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
    </header>

    <div v-if="activePage === 'workspace'" class="body-shell" :class="{ 'sidebar-pinned': sidebarPinned && sidebarOpen }" :style="bodyShellStyle">
      <div v-if="sidebarOpen && !sidebarPinned" class="sidebar-backdrop" @click="sidebarOpen = false"></div>
      <aside class="sidebar-drawer" :class="{ 'panel-open': sidebarOpen, pinned: sidebarPinned && sidebarOpen }">
        <FileSidebar
          :workspace-count="workspaceCount"
          :active-workspace-id="workspaces.activeWorkspaceId"
          :codex-session-ids="workspaces.activeCodexSessionIds"
          :panel-open="sidebarOpen"
          :panel-pinned="sidebarPinned"
          :workspace-notices="workspaceNotices"
          :switching-workspace="workspaces.switching"
          @open-file="openFile"
          @open-terminal="openTerminal"
          @open-codex-session="openCodexSession"
          @switch-workspace="switchWorkspace"
          @toggle-tool-panel="toggleToolPanel"
          @toggle-pin="toggleSidebarPin"
          @close-panel="sidebarOpen = false"
        />
      </aside>
      <div
        v-if="sidebarPinned && sidebarOpen"
        class="sidebar-resizer"
        role="separator"
        title="Drag to resize"
        @pointerdown="startSidebarResize"
      ></div>
      <main class="workspace-wrap">
        <Workspace />
      </main>
    </div>
    <main v-else-if="activePage === 'settings'" class="top-level-page">
      <ConfigPanel @close="activePage = 'workspace'" />
    </main>
    <main v-else class="top-level-page">
      <LoopTasksPage />
    </main>
  </div>
</template>
