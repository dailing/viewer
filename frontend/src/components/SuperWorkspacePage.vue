<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import {
  activateSuperChat,
  createSuperChat,
  createSuperRole,
  deleteSuperRole,
  deleteSuperChat,
  getSuperWorkspace,
  listSuperChats,
  listSuperWorkspaces,
  updateSuperRole,
  updateSuperChat,
} from "../api/client";
import { useAgentsStore } from "../stores/agents";
import { useFilesStore } from "../stores/files";
import { useLayoutStore } from "../stores/layout";
import type { SuperChatSummary, SuperRole } from "../types/superWorkspace";
import { namespacedStorageKey } from "../utils/userProfile";
import FileSidebar from "./FileSidebar.vue";
import Workspace from "./Workspace.vue";

const SIDEBAR_PIN_KEY = "viewer.superSidebarPinned.v1";
const SIDEBAR_WIDTH_KEY = "viewer.superSidebarWidth.v1";
const SIDEBAR_MIN_WIDTH = 220;
const SIDEBAR_MAX_WIDTH = 640;

const files = useFilesStore();
const agents = useAgentsStore();
const layout = useLayoutStore();
const activeWorkspaceId = ref("");
const chats = ref<SuperChatSummary[]>([]);
const activeChatId = ref("");
const roles = ref<SuperRole[]>([]);
const sidebarOpen = ref(true);
const sidebarPinned = ref(true);
const sidebarWidth = ref(320);
const isMobile = ref(false);
const loading = ref(false);
const error = ref("");
const bodyShellStyle = computed(() => ({ "--sidebar-width": `${sidebarWidth.value}px` }));
const effectiveSidebarPinned = computed(() => sidebarPinned.value && sidebarOpen.value && !isMobile.value);

let mobileQuery: MediaQueryList | null = null;
let chatActivationRequest = 0;

onMounted(async () => {
  layout.load("super-workspace");
  mobileQuery = window.matchMedia("(max-width: 767.98px)");
  isMobile.value = mobileQuery.matches;
  mobileQuery.addEventListener("change", handleViewportChange);
  sidebarPinned.value = localStorage.getItem(namespacedStorageKey(SIDEBAR_PIN_KEY)) !== "false";
  sidebarOpen.value = sidebarPinned.value && !isMobile.value;
  sidebarWidth.value = clampSidebarWidth(Number(localStorage.getItem(namespacedStorageKey(SIDEBAR_WIDTH_KEY))) || sidebarWidth.value);
  await agents.loadProviders();
  await loadState();
});

onUnmounted(() => {
  mobileQuery?.removeEventListener("change", handleViewportChange);
  layout.setStorageScope("");
});

watch(sidebarPinned, (pinned) => {
  localStorage.setItem(namespacedStorageKey(SIDEBAR_PIN_KEY), String(pinned));
  if (pinned && !isMobile.value) sidebarOpen.value = true;
});

function handleViewportChange(event: MediaQueryListEvent) {
  isMobile.value = event.matches;
  if (event.matches) {
    sidebarOpen.value = false;
    return;
  }
  if (sidebarPinned.value) sidebarOpen.value = true;
}

async function loadState() {
  loading.value = true;
  error.value = "";
  try {
    const [workspaceData, chatData, superData] = await Promise.all([listSuperWorkspaces(), listSuperChats(), getSuperWorkspace()]);
    activeWorkspaceId.value = workspaceData.active_workspace_id;
    chats.value = chatData.chats;
    activeChatId.value = chatData.active_chat_id;
    roles.value = superData.roles;
    notifyChatListChanged();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    loading.value = false;
  }
}

async function addChat() {
  error.value = "";
  try {
    const data = await createSuperChat({ name: `Chat ${chats.value.length + 1}`, type: "group" });
    chats.value = data.chats;
    activeChatId.value = data.active_chat_id;
    notifyChatListChanged();
    if (activeChatId.value) layout.openChat(activeChatId.value);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function openChat(chatId: string) {
  const requestId = ++chatActivationRequest;
  error.value = "";
  activeChatId.value = chatId;
  notifyChatListChanged();
  layout.openChat(chatId);
  if (!sidebarPinned.value) sidebarOpen.value = false;
  try {
    const data = await activateSuperChat(chatId);
    if (requestId !== chatActivationRequest) return;
    chats.value = data.chats;
    activeChatId.value = data.active_chat_id;
    notifyChatListChanged();
  } catch (err) {
    if (requestId !== chatActivationRequest) return;
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function saveChat(chat: SuperChatSummary) {
  error.value = "";
  try {
    const data = await updateSuperChat(chat.id, {
      name: chat.name,
      type: chat.type,
      pinned: chat.pinned,
      cwd: chat.cwd,
      common_prompt: chat.common_prompt,
      member_role_ids: chat.member_role_ids,
    });
    chats.value = data.chats;
    activeChatId.value = data.active_chat_id;
    notifyChatListChanged();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function addRole() {
  error.value = "";
  try {
    const data = await createSuperRole({ name: `Role ${roles.value.length + 1}`, provider: "codex" });
    roles.value = data.roles;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function saveRole(role: SuperRole) {
  error.value = "";
  try {
    const data = await updateSuperRole(role.id, {
      name: role.name,
      description: role.description,
      provider: role.provider,
      cwd: role.cwd,
      model: role.model ?? null,
      session_policy: role.session_policy,
    });
    roles.value = data.roles;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function removeRole(role: SuperRole) {
  if (!window.confirm(`Delete role "${role.name}"?`)) return;
  error.value = "";
  try {
    const data = await deleteSuperRole(role.id);
    roles.value = data.roles;
    const affected = chats.value
      .filter((chat) => chat.member_role_ids.includes(role.id))
      .map((chat) => ({ ...chat, member_role_ids: chat.member_role_ids.filter((roleId) => roleId !== role.id) }));
    for (const chat of affected) {
      await updateSuperChat(chat.id, { member_role_ids: chat.member_role_ids });
    }
    if (affected.length) {
      chats.value = (await listSuperChats()).chats;
      notifyChatListChanged();
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function removeChat(chat: SuperChatSummary) {
  if (!window.confirm(`Delete chat "${chat.name}"?`)) return;
  error.value = "";
  try {
    const data = await deleteSuperChat(chat.id);
    chats.value = data.chats;
    activeChatId.value = data.active_chat_id;
    notifyChatListChanged();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function notifyChatListChanged() {
  window.dispatchEvent(
    new CustomEvent("super-workspace:chats-updated", {
      detail: { chats: chats.value, activeChatId: activeChatId.value },
    }),
  );
}

function openFile(path: string) {
  void files.recordVisit(path);
  layout.openFile(path);
  if (!sidebarPinned.value) sidebarOpen.value = false;
}

function openTerminal(id: string) {
  layout.openTerminal(id);
  if (!sidebarPinned.value) sidebarOpen.value = false;
}

function openDiff(path: string, cwd = "") {
  layout.openDiff(path, cwd);
  if (!sidebarPinned.value) sidebarOpen.value = false;
}

function toggleToolPanel() {
  sidebarOpen.value = !sidebarOpen.value;
}

function toggleSidebarPin() {
  sidebarPinned.value = !sidebarPinned.value;
}

function clampSidebarWidth(value: number) {
  if (!Number.isFinite(value)) return 320;
  return Math.min(SIDEBAR_MAX_WIDTH, Math.max(SIDEBAR_MIN_WIDTH, value));
}

function startSidebarResize(event: PointerEvent) {
  event.preventDefault();
  const startX = event.clientX;
  const startWidth = sidebarWidth.value;
  document.body.classList.add("sidebar-resizing");

  const resize = (moveEvent: PointerEvent) => {
    sidebarWidth.value = clampSidebarWidth(startWidth + moveEvent.clientX - startX);
    localStorage.setItem(namespacedStorageKey(SIDEBAR_WIDTH_KEY), String(sidebarWidth.value));
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
</script>

<template>
  <div class="super-workspace-shell" :class="{ 'sidebar-pinned': effectiveSidebarPinned }" :style="bodyShellStyle">
    <div v-if="sidebarOpen && !effectiveSidebarPinned" class="sidebar-backdrop" @click="sidebarOpen = false"></div>
    <aside class="sidebar-drawer" :class="{ 'panel-open': sidebarOpen, pinned: effectiveSidebarPinned }">
      <FileSidebar
        :chats="chats"
        :active-chat-id="activeChatId"
        :roles="roles"
        :providers="agents.providers"
        :panel-open="sidebarOpen"
        :panel-pinned="effectiveSidebarPinned"
        @open-file="openFile"
        @open-terminal="openTerminal"
        @open-diff="openDiff"
        @open-chat="openChat"
        @create-chat="addChat"
        @update-chat="saveChat"
        @delete-chat="removeChat"
        @create-role="addRole"
        @update-role="saveRole"
        @delete-role="removeRole"
        @toggle-tool-panel="toggleToolPanel"
        @toggle-pin="toggleSidebarPin"
        @close-panel="sidebarOpen = false"
      />
    </aside>
    <div
      v-if="effectiveSidebarPinned"
      class="sidebar-resizer"
      role="separator"
      title="Drag to resize"
      @pointerdown="startSidebarResize"
    ></div>
    <main class="workspace-wrap">
      <div v-if="error" class="super-error">{{ error }}</div>
      <Workspace :loading="loading" :workspace-id="activeWorkspaceId || 'super-workspace'" />
    </main>
  </div>
</template>

<style scoped>
.super-workspace-shell {
  background: var(--color-surface);
  display: flex;
  flex: 1 1 auto;
  height: 100%;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
  position: relative;
}

.sidebar-drawer {
  display: flex;
  flex: 0 0 42px;
  min-height: 0;
  position: relative;
  width: 42px;
  z-index: 5;
}

.sidebar-drawer.pinned {
  flex: 0 0 var(--sidebar-width);
  width: var(--sidebar-width);
}

.sidebar-drawer.panel-open:not(.pinned) {
  z-index: 20;
}

.sidebar-backdrop {
  background: transparent;
  inset: 0;
  position: absolute;
  z-index: 4;
}

.sidebar-resizer {
  background: var(--color-surface-muted);
  cursor: col-resize;
  flex: 0 0 3px;
  position: relative;
  z-index: 6;
}

.sidebar-resizer:hover {
  background: var(--color-accent-soft);
}

.workspace-wrap {
  background: var(--color-surface);
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  position: relative;
}

.super-error {
  background: color-mix(in srgb, var(--color-danger) 10%, var(--color-surface));
  border: 1px solid color-mix(in srgb, var(--color-danger) 35%, var(--color-border));
  border-radius: var(--radius-sm);
  color: var(--color-danger);
  font-size: 12px;
  left: 16px;
  max-width: min(520px, calc(100% - 32px));
  padding: 8px 10px;
  position: absolute;
  top: 16px;
  z-index: 10;
}

@media (max-width: 767.98px) {
  .sidebar-drawer.pinned {
    flex: 0 0 42px;
    width: 42px;
  }

  .sidebar-resizer {
    display: none;
  }
}
</style>
