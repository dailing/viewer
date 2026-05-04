<script setup lang="ts">
import { computed, ref, watch } from "vue";
import CodexSessionsPanel from "./sidebar/CodexSessionsPanel.vue";
import FilesPanel from "./sidebar/FilesPanel.vue";
import TerminalsPanel from "./sidebar/TerminalsPanel.vue";

type SidebarTool = "files" | "terminals" | "codex";
type WorkspaceNotice = "completed" | "failed";

const ACTIVE_TOOL_KEY = "viewer.sidebarActiveTool.v1";

const props = defineProps<{
  workspaceCount: number;
  activeWorkspaceId: string;
  panelOpen: boolean;
  panelPinned: boolean;
  workspaceNotices?: Record<string, WorkspaceNotice>;
  switchingWorkspace?: boolean;
}>();

const emit = defineEmits<{
  "open-file": [path: string];
  "open-terminal": [id: string];
  "open-codex-session": [id: string];
  "switch-workspace": [id: string];
  "toggle-tool-panel": [];
  "toggle-pin": [];
  "close-panel": [];
}>();

const tools: Array<{ id: SidebarTool; title: string; icon: string }> = [
  { id: "files", title: "Files", icon: "bi-files" },
  { id: "terminals", title: "Terminals", icon: "bi-terminal" },
  { id: "codex", title: "Codex", icon: "bi-stars" },
];

function storedTool(): SidebarTool {
  const value = localStorage.getItem(ACTIVE_TOOL_KEY);
  return tools.some((tool) => tool.id === value) ? (value as SidebarTool) : "files";
}

const activeTool = ref<SidebarTool>(storedTool());
const activeTitle = computed(() => tools.find((tool) => tool.id === activeTool.value)?.title ?? "Files");
const workspaceIds = computed(() => Array.from({ length: Math.max(1, props.workspaceCount) }, (_, index) => String(index + 1)));

watch(activeTool, (tool) => {
  localStorage.setItem(ACTIVE_TOOL_KEY, tool);
});

function selectTool(tool: SidebarTool) {
  if (activeTool.value === tool) {
    emit("toggle-tool-panel");
    return;
  }
  activeTool.value = tool;
}

function selectWorkspace(id: string) {
  if (props.activeWorkspaceId === id) {
    emit("toggle-tool-panel");
    return;
  }
  emit("switch-workspace", id);
}

function workspaceTitle(id: string) {
  const notice = props.workspaceNotices?.[id];
  if (notice === "failed") return `Workspace ${id}: Codex run failed`;
  if (notice === "completed") return `Workspace ${id}: Codex run finished`;
  return `Workspace ${id}`;
}

function workspaceNoticeClass(id: string) {
  const notice = props.workspaceNotices?.[id];
  return notice ? `workspace-notice-${notice}` : "";
}
</script>

<template>
  <div class="tool-sidebar" :class="{ 'panel-open': props.panelOpen, 'panel-pinned': props.panelPinned }">
    <nav class="activity-bar" aria-label="Sidebar tools">
      <button
        v-for="tool in tools"
        :key="tool.id"
        class="activity-button"
        :class="{ active: activeTool === tool.id }"
        type="button"
        :title="tool.title"
        :aria-label="tool.title"
        :aria-pressed="activeTool === tool.id"
        @click="selectTool(tool.id)"
      >
        <i class="bi" :class="tool.icon"></i>
      </button>
      <div class="workspace-buttons" aria-label="Workspaces">
        <button
          v-for="id in workspaceIds"
          :key="id"
          class="activity-button workspace-button"
          :class="[{ active: props.activeWorkspaceId === id }, workspaceNoticeClass(id)]"
          type="button"
          :title="workspaceTitle(id)"
          :aria-label="workspaceTitle(id)"
          :aria-pressed="props.activeWorkspaceId === id"
          :disabled="props.switchingWorkspace"
          @click="selectWorkspace(id)"
        >
          <span>{{ id }}</span>
        </button>
      </div>
    </nav>

    <section v-if="props.panelOpen" class="tool-panel" :aria-label="activeTitle">
      <div class="tool-panel-title">
        <span>{{ activeTitle }}</span>
        <button
          class="panel-title-button"
          type="button"
          :title="props.panelPinned ? 'Unpin panel' : 'Pin panel'"
          :aria-label="props.panelPinned ? 'Unpin panel' : 'Pin panel'"
          @click="emit('toggle-pin')"
        >
          <i class="bi" :class="props.panelPinned ? 'bi-pin-angle-fill' : 'bi-pin-angle'"></i>
        </button>
        <button
          v-if="!props.panelPinned"
          class="panel-title-button"
          type="button"
          title="Hide panel"
          aria-label="Hide panel"
          @click="emit('close-panel')"
        >
          <i class="bi bi-x"></i>
        </button>
      </div>
      <FilesPanel v-if="activeTool === 'files'" @open-file="emit('open-file', $event)" />
      <TerminalsPanel v-else-if="activeTool === 'terminals'" @open-terminal="emit('open-terminal', $event)" />
      <CodexSessionsPanel v-else @open-codex-session="emit('open-codex-session', $event)" />
    </section>
  </div>
</template>

<style scoped>
.tool-sidebar {
  display: flex;
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  overflow: visible;
  position: relative;
}

.activity-bar {
  align-items: center;
  background: #f3f6fa;
  border-right: 1px solid var(--border);
  display: flex;
  flex: 0 0 42px;
  flex-direction: column;
  gap: 4px;
  padding: 6px 4px;
  position: relative;
  z-index: 2;
}

.activity-button {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: 6px;
  color: #5f6f86;
  display: inline-flex;
  height: 34px;
  justify-content: center;
  padding: 0;
  position: relative;
  width: 34px;
}

.activity-button:hover {
  background: #e8edf5;
  color: #1f2937;
}

.activity-button.active {
  background: #ffffff;
  color: #174ea6;
}

.activity-button.active::before {
  background: #1f6feb;
  border-radius: 999px;
  content: "";
  height: 22px;
  left: -4px;
  position: absolute;
  width: 3px;
}

.activity-button .bi {
  font-size: 16px;
  line-height: 1;
}

.workspace-buttons {
  border-top: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-top: 4px;
  padding-top: 6px;
}

.workspace-button {
  font-size: 12px;
  font-weight: 700;
}

.workspace-button.workspace-notice-completed:not(.active) {
  background: #fff4c2;
  color: #7a4f00;
}

.workspace-button.workspace-notice-failed:not(.active) {
  background: #ffe0e0;
  color: #a33;
}

.workspace-button.workspace-notice-completed:not(.active)::after,
.workspace-button.workspace-notice-failed:not(.active)::after {
  border: 2px solid #f3f6fa;
  border-radius: 999px;
  content: "";
  height: 9px;
  position: absolute;
  right: 4px;
  top: 4px;
  width: 9px;
}

.workspace-button.workspace-notice-completed:not(.active)::after {
  background: #f0ad00;
}

.workspace-button.workspace-notice-failed:not(.active)::after {
  background: #d1242f;
}

.tool-panel {
  background: #ffffff;
  display: flex;
  flex-direction: column;
  min-height: 0;
  min-width: 0;
  z-index: 3;
}

.tool-sidebar:not(.panel-pinned) .tool-panel {
  bottom: 0;
  box-shadow: 8px 0 24px rgb(15 23 42 / 0.16);
  left: 42px;
  max-width: calc(88vw - 42px);
  position: absolute;
  top: 0;
  width: calc(var(--sidebar-width) - 42px);
}

.tool-sidebar.panel-pinned .tool-panel {
  border-right: 1px solid var(--border);
  flex: 1 1 auto;
}

.tool-panel-title {
  align-items: center;
  border-bottom: 1px solid var(--border);
  color: #1f2937;
  display: flex;
  flex: 0 0 30px;
  font-size: 12px;
  font-weight: 700;
  gap: 4px;
  line-height: 30px;
  overflow: hidden;
  padding: 3px 6px 3px 10px;
}

.tool-panel-title span {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.panel-title-button {
  align-items: center;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 4px;
  color: #5f6f86;
  display: inline-flex;
  flex: 0 0 22px;
  height: 22px;
  justify-content: center;
  padding: 0;
  width: 22px;
}

.panel-title-button:hover {
  background: #e8edf5;
  color: #1f2937;
}

.panel-title-button .bi {
  font-size: 12px;
  line-height: 1;
}

@media (max-width: 767.98px) {
  .tool-sidebar.panel-pinned {
    flex: 0 0 42px;
    width: 42px;
  }

  .tool-sidebar.panel-pinned .tool-panel,
  .tool-sidebar:not(.panel-pinned) .tool-panel {
    bottom: 0;
    box-shadow: 8px 0 24px rgb(15 23 42 / 0.16);
    left: 42px;
    max-width: calc(100vw - 42px);
    position: absolute;
    top: 0;
    width: calc(100vw - 42px);
  }
}
</style>
