<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import AgentTasksPage from "./components/AgentTasksPage.vue";
import ConfigPanel from "./components/ConfigPanel.vue";
import FileSidebar from "./components/FileSidebar.vue";
import LoopTasksPage from "./components/LoopTasksPage.vue";
import Workspace from "./components/Workspace.vue";
import { connectEvents } from "./api/events";
import { useAgentsStore } from "./stores/agents";
import { useCodexStore } from "./stores/codex";
import { useFilesStore } from "./stores/files";
import { useLayoutStore } from "./stores/layout";
import { usePaneToolbarStore } from "./stores/paneToolbar";
import { useTerminalsStore } from "./stores/terminals";
import { useUsersStore } from "./stores/users";
import { useWorkspacesStore } from "./stores/workspaces";
import type { PaneToolbarAction, PaneToolbarControl } from "./stores/paneToolbar";
import type { AgentSessionInfo, AgentStatus } from "./types/agents";
import type { LayoutNode, SplitDirection } from "./types/layout";
import { agentRef, legacyAgentRefForPane, parseAgentRef } from "./utils/agents";
import { namespacedStorageKey } from "./utils/userProfile";

const SIDEBAR_PIN_KEY = "viewer.sidebarPinned.v1";
const SIDEBAR_WIDTH_KEY = "viewer.sidebarWidth.v1";
const WORKSPACE_HEAT_KEY = "viewer.workspaceHeat.v1";
const SIDEBAR_MIN_WIDTH = 220;
const SIDEBAR_MAX_WIDTH = 640;
type WorkspaceNotice = "completed" | "failed" | "running";
const files = useFilesStore();
const agents = useAgentsStore();
const codex = useCodexStore();
const layout = useLayoutStore();
const paneToolbar = usePaneToolbarStore();
const terminals = useTerminalsStore();
const users = useUsersStore();
const workspaces = useWorkspacesStore();
const appReady = ref(false);
const selectingUser = ref(false);
const sidebarOpen = ref(false);
const sidebarPinned = ref(false);
const activePage = ref<"workspace" | "settings" | "loops" | "tasks">("workspace");
const mobileToolbarOpen = ref(false);
const agentStatusByRef = ref<Record<string, AgentStatus>>({});
const workspaceHeat = ref<Record<string, number>>({});
const switchingWorkspaceId = ref<string | null>(null);
const workspaceContentLoading = ref(false);
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
  const paneAgent = legacyAgentRefForPane(pane);
  if (paneAgent) {
    const session = agents.sessions.find((item) => item.ref === paneAgent);
    const parsed = parseAgentRef(paneAgent);
    return session?.title ?? (parsed ? agents.providerById(parsed.provider).name : "Agent");
  }
  if (pane.diffPath) return `Diff: ${pane.diffPath}`;
  return pane.filePath || "Empty pane";
});
const activePaneHasContent = computed(() => {
  const pane = layout.activePane;
  return Boolean(pane?.type === "pane" && (pane.filePath || pane.terminalId || legacyAgentRefForPane(pane) || pane.diffPath));
});
const globalPaneActions = computed<PaneToolbarAction[]>(() => {
  if (!layout.activePaneId) return [];
  return [
    ...(layout.activePaneCanGoBack
      ? [
          {
            id: "go-back",
            title: "Go back",
            icon: "bi-arrow-left",
            run: () => goBackActivePane(),
          },
        ]
      : []),
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
const workspaceCount = computed(() => workspaces.count);
const displayedActiveWorkspaceId = computed(() => switchingWorkspaceId.value ?? workspaces.activeWorkspaceId);
const workspaceNotices = computed<Record<string, WorkspaceNotice>>(() => {
  const notices: Record<string, WorkspaceNotice> = {};
  for (const workspaceId of workspaceHeatIds()) {
    let notice: WorkspaceNotice | null = null;
    for (const ref of workspaceAgentSessionRefs(workspaceId)) {
      const session = agents.sessions.find((item) => item.ref === ref);
      if (!session) continue;
      notice = higherPriorityWorkspaceNotice(notice, noticeForAgentSession(session));
      if (notice === "failed") break;
    }
    if (notice) notices[workspaceId] = notice;
  }
  return notices;
});
let source: EventSource | null = null;
let terminalRefresh: number | null = null;
let codexRefresh: number | null = null;
let agentRefresh: number | null = null;
let workspaceHeatTimer: number | null = null;
let workspaceSaveTimer: number | null = null;
let workspaceAutosaveReady = false;
let workspaceSwitchToken = 0;

onMounted(async () => {
  await users.load();
  if (users.needsSelection) {
    selectingUser.value = true;
    return;
  }
  await initializeApp();
});

async function initializeApp() {
  appReady.value = false;
  sidebarPinned.value = localStorage.getItem(namespacedStorageKey(SIDEBAR_PIN_KEY)) === "true";
  sidebarWidth.value = clampSidebarWidth(Number(localStorage.getItem(namespacedStorageKey(SIDEBAR_WIDTH_KEY))) || sidebarWidth.value);
  sidebarOpen.value = sidebarPinned.value;
  files.currentPath = users.activeProfile?.home_path ?? "";
  layout.load();
  await Promise.all([files.loadConfig(), terminals.load(), codex.loadOptions(), agents.load(), workspaces.load()]);
  loadWorkspaceHeat();
  await restoreInitialWorkspace();
  updateWorkspaceAgentNotices();
  startWorkspaceHeatTimer();
  workspaceAutosaveReady = true;
  source = connectEvents(
    async (event) => {
      await files.refreshAffected(event.path, event.is_dir);
      window.dispatchEvent(new CustomEvent("viewer:file-changed", { detail: event }));
    },
  );
  terminalRefresh = window.setInterval(() => {
    void terminals.load();
  }, 3000);
  codexRefresh = window.setInterval(() => {
    void codex.loadOptions();
  }, 10000);
  agentRefresh = window.setInterval(() => {
    void agents.load();
  }, 5000);
  appReady.value = true;
}

async function selectUserProfile(userId: string) {
  users.select(userId);
  selectingUser.value = false;
  await initializeApp();
}

watch(
  [() => layout.root, () => layout.activePaneId, () => files.currentPath, () => files.pinned, () => files.visitTimes],
  () => {
    scheduleWorkspaceSave();
  },
  { deep: true },
);

watch(workspaceCount, () => {
  if (!workspaceAutosaveReady) return;
  normalizeWorkspaceHeat();
  const activeId = normalizeWorkspaceId(workspaces.activeWorkspaceId);
  if (activeId !== workspaces.activeWorkspaceId) {
    void switchWorkspace(activeId);
  }
});

watch(
  () => [files.workspaceConfig.heat_interval_seconds, files.workspaceConfig.heat_step_percent],
  () => {
    if (!workspaceAutosaveReady) return;
    startWorkspaceHeatTimer();
  },
);

watch(sidebarPinned, (pinned) => {
  localStorage.setItem(namespacedStorageKey(SIDEBAR_PIN_KEY), String(pinned));
  if (pinned) sidebarOpen.value = true;
});

watch(
  () => agents.sessions.map((session) => `${session.ref}:${session.status}`).join("|"),
  () => {
    updateWorkspaceAgentNotices();
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

function openAgentSession(ref: string) {
  agents.markRead(ref);
  void workspaces.rememberActiveAgentSession(ref);
  layout.openAgentSession(ref);
  if (!sidebarPinned.value) sidebarOpen.value = false;
}

function openDiff(path: string, cwd = "") {
  layout.openDiff(path, cwd);
  if (!sidebarPinned.value) sidebarOpen.value = false;
}

function currentWorkspaceSnapshot() {
  const snapshot = layout.snapshot();
  return {
    layout: snapshot.root,
    active_pane_id: snapshot.activePaneId,
    current_path: files.currentPath,
    pinned: [...files.pinned],
    visit_times: { ...files.visitTimes },
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

async function showWorkspaceLoadingFrame() {
  await nextTick();
  await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
}

function normalizeWorkspaceId(id: string) {
  const index = Number(id);
  if (!Number.isInteger(index) || index < 1 || index > workspaceCount.value) return "1";
  return String(index);
}

function workspaceHeatIds() {
  return Array.from({ length: Math.max(1, workspaceCount.value) }, (_, index) => String(index + 1));
}

function clampHeat(value: unknown) {
  const next = Number(value);
  if (!Number.isFinite(next)) return 0;
  return Math.min(1, Math.max(0, next));
}

function heatStep() {
  return Math.min(1, Math.max(0.001, files.workspaceConfig.heat_step_percent / 100));
}

function heatIntervalMs() {
  return Math.max(1000, files.workspaceConfig.heat_interval_seconds * 1000);
}

function normalizeWorkspaceHeat() {
  const next: Record<string, number> = {};
  for (const id of workspaceHeatIds()) {
    next[id] = clampHeat(workspaceHeat.value[id]);
  }
  workspaceHeat.value = next;
  saveWorkspaceHeat();
}

function loadWorkspaceHeat() {
  try {
    const parsed = JSON.parse(localStorage.getItem(namespacedStorageKey(WORKSPACE_HEAT_KEY)) || "{}");
    const values = typeof parsed?.values === "object" && parsed.values ? parsed.values : parsed;
    const elapsedMs = Math.max(0, Date.now() - Number(parsed?.updated_at ?? Date.now()));
    const intervals = Math.floor(elapsedMs / heatIntervalMs());
    const decay = Math.pow(1 - heatStep(), intervals);
    const next: Record<string, number> = {};
    for (const id of workspaceHeatIds()) {
      const value = clampHeat(values?.[id]);
      next[id] = id === workspaces.activeWorkspaceId ? 1 - (1 - value) * decay : value * decay;
    }
    workspaceHeat.value = next;
  } catch {
    workspaceHeat.value = {};
    normalizeWorkspaceHeat();
  }
  saveWorkspaceHeat();
}

function saveWorkspaceHeat() {
  localStorage.setItem(namespacedStorageKey(WORKSPACE_HEAT_KEY), JSON.stringify({ values: workspaceHeat.value, updated_at: Date.now() }));
}

function tickWorkspaceHeat() {
  const activeId = displayedActiveWorkspaceId.value;
  const step = heatStep();
  const next: Record<string, number> = {};
  for (const id of workspaceHeatIds()) {
    const current = clampHeat(workspaceHeat.value[id]);
    next[id] = id === activeId ? current + (1 - current) * step : current * (1 - step);
  }
  workspaceHeat.value = next;
  saveWorkspaceHeat();
}

function startWorkspaceHeatTimer() {
  if (workspaceHeatTimer !== null) window.clearInterval(workspaceHeatTimer);
  tickWorkspaceHeat();
  workspaceHeatTimer = window.setInterval(tickWorkspaceHeat, heatIntervalMs());
}

async function loadWorkspaceDirectory(path: string) {
  const targetPath = path || "";
  files.currentPath = targetPath;
  files.visitTimes = { ...files.visitTimes, [targetPath]: Date.now() / 1000 };
  try {
    await files.loadDirectory(targetPath);
  } catch {
    files.currentPath = "";
    files.visitTimes = { ...files.visitTimes, "": Date.now() / 1000 };
    await files.loadDirectory("");
  }
}

function restoreWorkspacePins(pinned?: string[] | null) {
  files.pinned = [...(pinned ?? files.pinned)];
}

function restoreWorkspaceVisitTimes(visitTimes?: Record<string, number> | null) {
  files.visitTimes = { ...(visitTimes ?? {}) };
}

async function restoreInitialWorkspace() {
  const activeId = normalizeWorkspaceId(workspaces.activeWorkspaceId);
  if (activeId !== workspaces.activeWorkspaceId) {
    await workspaces.activate(activeId);
  }
  const snapshot = workspaces.snapshotFor(activeId);
  if (snapshot) {
    layout.restore(snapshot.layout, snapshot.active_pane_id);
    workspaces.restoreActiveAgentSessions(snapshot);
    restoreWorkspacePins(snapshot.pinned);
    restoreWorkspaceVisitTimes(snapshot.visit_times);
    await loadWorkspaceDirectory(snapshot.current_path);
    return;
  }
  await files.loadDirectory(files.currentPath);
  workspaces.restoreActiveAgentSessions(currentWorkspaceSnapshot());
  await workspaces.saveSlot(activeId, currentWorkspaceSnapshot());
}

async function switchWorkspace(id: string) {
  const targetId = normalizeWorkspaceId(id);
  if (targetId === displayedActiveWorkspaceId.value) return;
  const token = ++workspaceSwitchToken;
  const previousId = workspaces.activeWorkspaceId;
  const previousSnapshot = currentWorkspaceSnapshot();
  window.dispatchEvent(new CustomEvent("viewer:workspace-before-switch", { detail: { workspaceId: previousId } }));
  workspaces.switching = true;
  switchingWorkspaceId.value = targetId;
  tickWorkspaceHeat();
  workspaceContentLoading.value = true;
  if (workspaceSaveTimer !== null) {
    window.clearTimeout(workspaceSaveTimer);
    workspaceSaveTimer = null;
  }
  const target = workspaces.snapshotFor(targetId);
  workspaces.activeWorkspaceId = targetId;
  if (target) {
    layout.restore(target.layout, target.active_pane_id);
    workspaces.restoreActiveAgentSessions(target);
    restoreWorkspacePins(target.pinned);
    restoreWorkspaceVisitTimes(target.visit_times);
    files.currentPath = target.current_path || "";
  } else {
    layout.reset();
    workspaces.restoreActiveAgentSessions(null);
    files.pinned = [];
    files.visitTimes = {};
    files.currentPath = "";
    workspaces.slots[targetId] = currentWorkspaceSnapshot();
  }
  await showWorkspaceLoadingFrame();
  if (token !== workspaceSwitchToken) return;
  workspaceContentLoading.value = false;
  workspaces.switching = false;
  switchingWorkspaceId.value = null;
  void persistWorkspaceSwitch(token, previousId, previousSnapshot, targetId, target);
}

async function persistWorkspaceSwitch(
  token: number,
  previousId: string,
  previousSnapshot: ReturnType<typeof currentWorkspaceSnapshot>,
  targetId: string,
  target: ReturnType<typeof workspaces.snapshotFor>,
) {
  try {
    await workspaces.saveSlot(previousId, previousSnapshot, { apply: false, restoreActive: false });
    if (token !== workspaceSwitchToken) return;
    if (target) {
      await loadWorkspaceDirectory(target.current_path);
    } else {
      const targetSnapshot = currentWorkspaceSnapshot();
      await workspaces.saveSlot(targetId, targetSnapshot, { apply: false, restoreActive: false });
    }
    if (token !== workspaceSwitchToken) return;
    await workspaces.activate(targetId, { apply: false });
    if (token !== workspaceSwitchToken) {
      void workspaces.activate(workspaces.activeWorkspaceId, { apply: false });
      return;
    }
  } finally {
    if (token === workspaceSwitchToken) {
      workspaces.activeWorkspaceId = targetId;
      workspaces.switching = false;
      switchingWorkspaceId.value = null;
      workspaceContentLoading.value = false;
    }
  }
}

function updateWorkspaceAgentNotices() {
  const previous = agentStatusByRef.value;
  const next: Record<string, AgentStatus> = {};

  for (const session of agents.sessions) {
    next[session.ref] = session.status;
    if (!["exited", "failed"].includes(session.status)) continue;
    for (const workspaceId of workspaceIdsForAgentSession(session.ref)) {
      if (previous[session.ref] !== "running" && !sessionFinishedAfterWorkspaceSaved(session, workspaceId)) continue;
      if (workspaceId === workspaces.activeWorkspaceId && activePaneAgentRef() === session.ref) {
        agents.markRead(session.ref);
        continue;
      }
      agents.markUnread(session.ref);
    }
  }

  agentStatusByRef.value = next;
}

function activePaneAgentRef() {
  const pane = layout.activePane;
  return pane?.type === "pane" ? legacyAgentRefForPane(pane) : undefined;
}

function noticePriority(notice: WorkspaceNotice | null) {
  if (notice === "failed") return 3;
  if (notice === "completed") return 2;
  if (notice === "running") return 1;
  return 0;
}

function higherPriorityWorkspaceNotice(current: WorkspaceNotice | null, next: WorkspaceNotice | null) {
  return noticePriority(next) > noticePriority(current) ? next : current;
}

function noticeForAgentSession(session: AgentSessionInfo): WorkspaceNotice | null {
  if (session.status === "failed" && agents.unreadSessionRefs.includes(session.ref)) return "failed";
  if (session.status === "exited" && agents.unreadSessionRefs.includes(session.ref)) return "completed";
  if (session.status === "running") return "running";
  return null;
}

function workspaceAgentSessionRefs(workspaceId: string) {
  const refs = new Set<string>();
  const snapshot = workspaceId === displayedActiveWorkspaceId.value ? null : workspaces.snapshotFor(workspaceId);
  const snapshotRefs = workspaceId === displayedActiveWorkspaceId.value ? workspaces.activeAgentSessionRefs : snapshot?.agent_session_ids ?? [];
  for (const ref of snapshotRefs) refs.add(ref);
  for (const id of snapshot?.codex_session_ids ?? []) refs.add(agentRef("codex", id));
  for (const id of snapshot?.hermes_session_ids ?? []) refs.add(agentRef("hermes", id));
  const root = workspaceId === displayedActiveWorkspaceId.value ? layout.root : snapshot?.layout;
  if (root) collectLayoutAgentSessionRefs(root, refs);
  return [...refs];
}

function workspaceIdsForAgentSession(ref: string) {
  const ids: string[] = [];
  for (let index = 1; index <= workspaceCount.value; index += 1) {
    const workspaceId = String(index);
    if (workspaceAgentSessionRefs(workspaceId).includes(ref)) ids.push(workspaceId);
  }
  return ids;
}

function collectLayoutAgentSessionRefs(node: LayoutNode, refs: Set<string>) {
  if (node.type === "pane") {
    const ref = legacyAgentRefForPane(node);
    if (ref) refs.add(ref);
    return;
  }
  collectLayoutAgentSessionRefs(node.first, refs);
  collectLayoutAgentSessionRefs(node.second, refs);
}

function sessionFinishedAfterWorkspaceSaved(session: AgentSessionInfo, workspaceId: string) {
  const snapshotUpdatedAt = workspaces.snapshotFor(workspaceId)?.updated_at;
  return Boolean(snapshotUpdatedAt && session.updated_at > snapshotUpdatedAt);
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
    localStorage.setItem(namespacedStorageKey(SIDEBAR_WIDTH_KEY), String(nextWidth));
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

function goBackActivePane() {
  layout.goBack();
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
  if (agentRefresh !== null) window.clearInterval(agentRefresh);
  if (workspaceHeatTimer !== null) window.clearInterval(workspaceHeatTimer);
});
</script>

<template>
  <div v-if="selectingUser" class="user-select-page">
    <section class="user-select-panel" aria-label="Select user profile">
      <h1>Select Profile</h1>
      <button
        v-for="profile in users.profiles"
        :key="profile.id"
        class="user-profile-button"
        type="button"
        @click="selectUserProfile(profile.id)"
      >
        <span>{{ profile.name || profile.id }}</span>
        <small>{{ profile.home || "/" }}</small>
      </button>
    </section>
  </div>
  <div v-else-if="appReady" class="app-shell" :style="appStyle">
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
      <button
        class="btn btn-outline-secondary icon-button"
        :class="{ active: activePage === 'tasks' }"
        type="button"
        title="Task DAG"
        @click="activePage = activePage === 'tasks' ? 'workspace' : 'tasks'"
      >
        <i class="bi bi-diagram-3"></i>
      </button>
      <div
        class="active-pane-title"
        :title="activePage === 'workspace' ? activePaneTitle : activePage === 'settings' ? 'Settings' : activePage === 'loops' ? 'Loop Tasks' : 'Task DAG'"
      >
        {{ activePage === 'workspace' ? activePaneTitle : activePage === 'settings' ? 'Settings' : activePage === 'loops' ? 'Loop Tasks' : 'Task DAG' }}
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
          :active-workspace-id="displayedActiveWorkspaceId"
          :agent-session-refs="workspaces.activeAgentSessionRefs"
          :panel-open="sidebarOpen"
          :panel-pinned="sidebarPinned"
          :workspace-notices="workspaceNotices"
          :workspace-heat="workspaceHeat"
          :switching-workspace="workspaces.switching"
          @open-file="openFile"
          @open-terminal="openTerminal"
          @open-agent-session="openAgentSession"
          @open-diff="openDiff"
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
        <Workspace :loading="workspaceContentLoading" :workspace-id="displayedActiveWorkspaceId" />
      </main>
    </div>
    <main v-else-if="activePage === 'settings'" class="top-level-page">
      <ConfigPanel @close="activePage = 'workspace'" />
    </main>
    <main v-else-if="activePage === 'loops'" class="top-level-page">
      <LoopTasksPage />
    </main>
    <main v-else class="top-level-page task-dag-top-page">
      <AgentTasksPage />
    </main>
  </div>
</template>
