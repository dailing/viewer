<script setup lang="ts">
import { computed, ref, watch } from "vue";
import ChatsPanel from "./sidebar/ChatsPanel.vue";
import FilesPanel from "./sidebar/FilesPanel.vue";
import GitPanel from "./sidebar/GitPanel.vue";
import RolesPanel from "./sidebar/RolesPanel.vue";
import TerminalsPanel from "./sidebar/TerminalsPanel.vue";
import { useFilesStore } from "../stores/files";
import { useLayoutStore } from "../stores/layout";
import { useTerminalsStore } from "../stores/terminals";
import { namespacedStorageKey } from "../utils/userProfile";
import type { AgentProvider } from "../types/agents";
import type { SuperChatSummary, SuperRole } from "../types/superWorkspace";

type SidebarTool = "files" | "git" | "terminals" | "chats" | "roles";

const ACTIVE_TOOL_KEY = "viewer.sidebarActiveTool.v1";

const props = defineProps<{
  chats?: SuperChatSummary[];
  activeChatId?: string;
  roles?: SuperRole[];
  providers?: { id: AgentProvider; name: string }[];
  panelOpen: boolean;
  panelPinned: boolean;
}>();

const emit = defineEmits<{
  "open-file": [path: string];
  "open-terminal": [id: string];
  "open-diff": [path: string, cwd: string];
  "open-chat": [id: string];
  "create-chat": [];
  "update-chat": [chat: SuperChatSummary];
  "delete-chat": [chat: SuperChatSummary];
  "create-role": [];
  "update-role": [role: SuperRole];
  "delete-role": [role: SuperRole];
  "toggle-tool-panel": [];
  "toggle-pin": [];
  "close-panel": [];
}>();

const tools: Array<{ id: SidebarTool; title: string; icon: string }> = [
  { id: "chats", title: "Chats", icon: "bi-chat-left-text" },
  { id: "roles", title: "Roles", icon: "bi-person-lines-fill" },
  { id: "files", title: "Files", icon: "bi-files" },
  { id: "git", title: "Changes", icon: "bi-git" },
  { id: "terminals", title: "Terminals", icon: "bi-terminal" },
];

const files = useFilesStore();
const layout = useLayoutStore();
const terminals = useTerminalsStore();

function storedTool(): SidebarTool {
  const value = localStorage.getItem(namespacedStorageKey(ACTIVE_TOOL_KEY));
  return tools.some((tool) => tool.id === value) ? (value as SidebarTool) : "chats";
}

const activeTool = ref<SidebarTool>(storedTool());
const activeTitle = computed(() => tools.find((tool) => tool.id === activeTool.value)?.title ?? "Files");
const currentChatId = computed(() => (layout.activePane?.type === "pane" ? layout.activePane.chatId ?? "" : "") || props.activeChatId || "");
const defaultToolCwd = computed(() => (props.chats ?? []).find((chat) => chat.id === currentChatId.value)?.cwd.trim() ?? "");
const pinnedChats = computed(() => (props.chats ?? []).filter((chat) => chat.pinned));
const pinnedFiles = computed(() => files.pinned);
const pinnedTerminals = computed(() => terminals.pinnedTerminals);

watch(activeTool, (tool) => {
  localStorage.setItem(namespacedStorageKey(ACTIVE_TOOL_KEY), tool);
});

function selectTool(tool: SidebarTool) {
  if (activeTool.value === tool) {
    emit("toggle-tool-panel");
    return;
  }
  activeTool.value = tool;
}

function chatInitial(chat: SuperChatSummary) {
  return (chat.name.trim()[0] ?? "#").toUpperCase();
}

function fileLabel(path: string) {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] || "/";
}

async function openPinnedFile(path: string) {
  try {
    await files.enterDirectory(path);
  } catch {
    emit("open-file", path);
  }
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
      <div v-if="pinnedFiles.length || pinnedTerminals.length || pinnedChats.length" class="workspace-buttons" aria-label="Pinned shortcuts">
        <button
          v-for="path in pinnedFiles"
          :key="`file:${path}`"
          class="activity-button workspace-button shortcut-button"
          :class="{ active: layout.openPaths.includes(path) }"
          type="button"
          :title="path"
          :aria-label="`Open ${path}`"
          @click="openPinnedFile(path)"
        >
          <i class="bi bi-file-earmark"></i>
          <span class="shortcut-label">{{ fileLabel(path) }}</span>
        </button>
        <button
          v-for="terminal in pinnedTerminals"
          :key="`terminal:${terminal.id}`"
          class="activity-button workspace-button shortcut-button"
          :class="{ active: layout.openTerminalIds.includes(terminal.id) }"
          type="button"
          :title="terminal.title"
          :aria-label="`Open ${terminal.title}`"
          @click="emit('open-terminal', terminal.id)"
        >
          <i class="bi" :class="terminal.status === 'running' ? 'bi-terminal-fill' : 'bi-terminal'"></i>
          <span class="shortcut-label">{{ terminal.title }}</span>
        </button>
        <button
          v-for="chat in pinnedChats"
          :key="`chat:${chat.id}`"
          class="activity-button workspace-button"
          :class="{ active: props.activeChatId === chat.id || layout.openChatIds.includes(chat.id) }"
          type="button"
          :title="chat.name"
          :aria-label="`Open ${chat.name}`"
          @click="emit('open-chat', chat.id)"
        >
          <span>{{ chatInitial(chat) }}</span>
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
      <FilesPanel v-if="activeTool === 'files'" :default-cwd="defaultToolCwd" @open-file="emit('open-file', $event)" />
      <GitPanel v-else-if="activeTool === 'git'" :default-cwd="defaultToolCwd" @open-diff="(path, cwd) => emit('open-diff', path, cwd)" />
      <TerminalsPanel v-else-if="activeTool === 'terminals'" :default-cwd="defaultToolCwd" @open-terminal="emit('open-terminal', $event)" />
      <ChatsPanel
        v-else-if="activeTool === 'chats'"
        :chats="props.chats ?? []"
        :active-chat-id="props.activeChatId ?? ''"
        :roles="props.roles ?? []"
        @create-chat="emit('create-chat')"
        @open-chat="emit('open-chat', $event)"
        @update-chat="emit('update-chat', $event)"
        @delete-chat="emit('delete-chat', $event)"
      />
      <RolesPanel
        v-else-if="activeTool === 'roles'"
        :roles="props.roles ?? []"
        :providers="props.providers ?? []"
        @create-role="emit('create-role')"
        @update-role="emit('update-role', $event)"
        @delete-role="emit('delete-role', $event)"
      />
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
  background: var(--color-surface-muted);
  border-right: 1px solid var(--color-border);
  display: flex;
  flex: 0 0 42px;
  flex-direction: column;
  gap: 4px;
  min-width: 42px;
  padding: 6px 4px;
  position: relative;
  width: 42px;
  z-index: 2;
}

.activity-button {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: 6px;
  color: var(--color-text-muted);
  display: inline-flex;
  flex: 0 0 34px;
  height: 34px;
  justify-content: center;
  padding: 0;
  position: relative;
  width: 34px;
}

.activity-button:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.activity-button.active {
  background: var(--color-surface);
  color: var(--color-accent-hover);
}

.activity-button.active::before {
  background: var(--color-accent);
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
  border-top: 1px solid var(--color-border);
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

.shortcut-button {
  flex-direction: column;
  gap: 0;
}

.shortcut-button .bi {
  font-size: 13px;
}

.shortcut-label {
  display: block;
  font-size: 8px;
  font-weight: 700;
  line-height: 1;
  max-width: 28px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.workspace-button.workspace-switching {
  color: transparent;
}

@keyframes workspace-spin {
  to {
    transform: rotate(360deg);
  }
}

.workspace-button.workspace-notice-running::after,
.workspace-button.workspace-notice-completed::after,
.workspace-button.workspace-notice-failed::after {
  border: 2px solid var(--color-surface-muted);
  border-radius: 999px;
  content: "";
  height: 9px;
  position: absolute;
  right: 4px;
  top: 4px;
  width: 9px;
}

.workspace-button.workspace-notice-running::after {
  background: var(--color-success);
}

.workspace-button.workspace-notice-completed::after {
  background: var(--color-warning);
}

.workspace-button.workspace-notice-failed::after {
  background: var(--color-danger);
}

.workspace-button.workspace-switching::after {
  animation: workspace-spin 0.75s linear infinite;
  background: transparent;
  border: 2px solid var(--color-border-strong);
  border-radius: 999px;
  border-top-color: var(--color-accent);
  content: "";
  height: 15px;
  position: absolute;
  width: 15px;
}

.tool-panel {
  background: var(--color-surface);
  display: flex;
  flex-direction: column;
  min-height: 0;
  min-width: 0;
  z-index: 3;
}

.tool-sidebar:not(.panel-pinned) .tool-panel {
  bottom: 0;
  box-shadow: var(--shadow-popover);
  left: 42px;
  max-width: calc(88vw - 42px);
  position: absolute;
  top: 0;
  width: calc(var(--sidebar-width) - 42px);
}

.tool-sidebar.panel-pinned .tool-panel {
  border-right: 1px solid var(--color-border);
  flex: 1 1 auto;
}

.tool-panel-title {
  align-items: center;
  border-bottom: 1px solid var(--color-border);
  color: var(--color-text);
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
  border: 1px solid var(--color-border);
  border-radius: 4px;
  color: var(--color-text-muted);
  display: inline-flex;
  flex: 0 0 22px;
  height: 22px;
  justify-content: center;
  padding: 0;
  width: 22px;
}

.panel-title-button:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
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
    box-shadow: var(--shadow-popover);
    left: 42px;
    max-width: calc(100vw - 42px);
    position: absolute;
    top: 0;
    width: calc(100vw - 42px);
  }
}
</style>
