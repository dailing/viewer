<script setup lang="ts">
import { computed, ref, watch } from "vue";
import CodexSessionsPanel from "./sidebar/CodexSessionsPanel.vue";
import FilesPanel from "./sidebar/FilesPanel.vue";
import TerminalsPanel from "./sidebar/TerminalsPanel.vue";

type SidebarTool = "files" | "terminals" | "codex";

const ACTIVE_TOOL_KEY = "viewer.sidebarActiveTool.v1";

const props = defineProps<{
  workspaceCount: number;
  activeWorkspaceId: string;
  switchingWorkspace?: boolean;
}>();

const emit = defineEmits<{
  "open-file": [path: string];
  "open-terminal": [id: string];
  "open-codex-session": [id: string];
  "switch-workspace": [id: string];
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
</script>

<template>
  <div class="tool-sidebar">
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
        @click="activeTool = tool.id"
      >
        <i class="bi" :class="tool.icon"></i>
      </button>
      <div class="workspace-buttons" aria-label="Workspaces">
        <button
          v-for="id in workspaceIds"
          :key="id"
          class="activity-button workspace-button"
          :class="{ active: props.activeWorkspaceId === id }"
          type="button"
          :title="`Workspace ${id}`"
          :aria-label="`Workspace ${id}`"
          :aria-pressed="props.activeWorkspaceId === id"
          :disabled="props.switchingWorkspace"
          @click="emit('switch-workspace', id)"
        >
          <span>{{ id }}</span>
        </button>
      </div>
    </nav>

    <section class="tool-panel" :aria-label="activeTitle">
      <div class="tool-panel-title">{{ activeTitle }}</div>
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

.tool-panel {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  min-height: 0;
  min-width: 0;
}

.tool-panel-title {
  border-bottom: 1px solid var(--border);
  color: #1f2937;
  flex: 0 0 30px;
  font-size: 12px;
  font-weight: 700;
  line-height: 30px;
  overflow: hidden;
  padding: 0 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
