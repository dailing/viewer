<script setup lang="ts">
import { computed, ref } from "vue";
import { useLayoutStore } from "../../stores/layout";
import { useSuperChatDispatchStore } from "../../stores/superChatDispatch";
import type { SuperChatSummary, SuperRole } from "../../types/superWorkspace";
import DirectoryPicker from "../DirectoryPicker.vue";

const props = defineProps<{
  chats: SuperChatSummary[];
  activeChatId: string;
  roles: SuperRole[];
}>();

const emit = defineEmits<{
  "create-chat": [];
  "open-chat": [id: string];
  "update-chat": [chat: SuperChatSummary];
  "delete-chat": [chat: SuperChatSummary];
}>();

const layout = useLayoutStore();
const dispatchSelection = useSuperChatDispatchStore();
const settingsChatId = ref("");
const selectedSettingsChat = computed(() => props.chats.find((chat) => chat.id === settingsChatId.value) ?? null);
const rolesById = computed(() => new Map(props.roles.map((role) => [role.id, role])));
const focusedChatId = computed(() => (layout.activePane?.type === "pane" ? layout.activePane.chatId ?? "" : ""));

function save(chat: SuperChatSummary) {
  normalizeDirectChat(chat);
  emit("update-chat", chat);
}

function toggleSettings(chat: SuperChatSummary) {
  settingsChatId.value = settingsChatId.value === chat.id ? "" : chat.id;
}

function toggleRole(chat: SuperChatSummary, roleId: string) {
  if (chat.type === "direct") {
    const previousRoleId = chat.member_role_ids[0] ?? "";
    chat.member_role_ids = [roleId];
    chat.name = rolesById.value.get(roleId)?.name ?? chat.name;
    if (previousRoleId && previousRoleId !== roleId) dispatchSelection.clearRole(chat.id, previousRoleId);
    emit("update-chat", chat);
    return;
  }
  const next = new Set(chat.member_role_ids ?? []);
  if (next.has(roleId)) {
    next.delete(roleId);
    dispatchSelection.clearRole(chat.id, roleId);
  } else {
    next.add(roleId);
  }
  chat.member_role_ids = [...next];
  emit("update-chat", chat);
}

function handleTypeChange(chat: SuperChatSummary) {
  normalizeDirectChat(chat);
  emit("update-chat", chat);
}

function normalizeDirectChat(chat: SuperChatSummary) {
  if (chat.type !== "direct") return;
  const currentRoleId = chat.member_role_ids.find((roleId) => rolesById.value.has(roleId));
  const fallbackRoleId = props.roles[0]?.id ?? "";
  const roleId = currentRoleId || fallbackRoleId;
  chat.member_role_ids = roleId ? [roleId] : [];
  const roleName = roleId ? rolesById.value.get(roleId)?.name : "";
  if (roleName) chat.name = roleName;
}

function chatMemberRoles(chat: SuperChatSummary) {
  return chat.member_role_ids.map((roleId) => rolesById.value.get(roleId)).filter((role): role is SuperRole => Boolean(role));
}

function isDispatchRoleSelected(chat: SuperChatSummary, roleId: string) {
  return dispatchSelection.isRoleSelected(chat.id, roleId);
}

function toggleDispatchRole(chat: SuperChatSummary, roleId: string) {
  dispatchSelection.toggleRole(chat.id, roleId);
}
</script>

<template>
  <div class="sidebar-panel">
    <div class="sidebar-section list-section">
      <div v-if="!props.chats.length" class="empty-panel">No chats</div>
      <div
        v-for="chat in props.chats"
        :key="chat.id"
        class="chat-entry"
      >
        <div class="sidebar-row" :class="{ active: chat.id === props.activeChatId || layout.openChatIds.includes(chat.id) }">
          <button class="sidebar-row-main" type="button" :title="chat.name" @click="emit('open-chat', chat.id)">
            <i class="bi" :class="chat.type === 'direct' ? 'bi-person' : 'bi-chat-left-text'"></i>
            <span class="sidebar-row-name">{{ chat.name }}</span>
          </button>
          <button class="btn btn-sm icon-button sidebar-row-action" type="button" title="Chat settings" @click="toggleSettings(chat)">
            <i class="bi bi-sliders"></i>
          </button>
        </div>
        <div v-if="focusedChatId === chat.id" class="dispatch-role-list" aria-label="Manual dispatch role">
          <div v-if="!chatMemberRoles(chat).length" class="dispatch-role-empty">No roles in chat</div>
          <button
            v-for="role in chatMemberRoles(chat)"
            :key="role.id"
            class="dispatch-role-chip"
            :class="{ selected: isDispatchRoleSelected(chat, role.id) }"
            type="button"
            :title="isDispatchRoleSelected(chat, role.id) ? `Cancel ${role.name}` : `Dispatch to ${role.name}`"
            @click="toggleDispatchRole(chat, role.id)"
          >
            <i class="bi bi-send"></i>
            <span>{{ role.name }}</span>
            <i v-if="isDispatchRoleSelected(chat, role.id)" class="bi bi-check2"></i>
          </button>
        </div>
      </div>
    </div>

    <div class="sidebar-action-footer">
      <button class="sidebar-add-button" type="button" title="New chat" aria-label="New chat" @click="emit('create-chat')">
        <i class="bi bi-plus-lg"></i>
      </button>
    </div>

    <form v-if="selectedSettingsChat" class="chat-settings" @submit.prevent="save(selectedSettingsChat)">
      <div class="settings-title">
        <span>Chat Settings</span>
        <button class="btn btn-sm icon-button" type="button" title="Close" @click="settingsChatId = ''">
          <i class="bi bi-x"></i>
        </button>
      </div>
      <label class="field">
        <span>Name</span>
        <input
          v-model="selectedSettingsChat.name"
          class="form-control form-control-sm"
          :readonly="selectedSettingsChat.type === 'direct'"
          :title="selectedSettingsChat.type === 'direct' ? 'Direct chat name follows the selected role' : ''"
        />
      </label>
      <label class="field">
        <span>Type</span>
        <select v-model="selectedSettingsChat.type" class="form-select form-select-sm" @change="handleTypeChange(selectedSettingsChat)">
          <option value="group">Group</option>
          <option value="direct">Direct</option>
        </select>
      </label>
      <label class="setting-check">
        <input v-model="selectedSettingsChat.pinned" class="form-check-input" type="checkbox" @change="save(selectedSettingsChat)" />
        <span>Pinned</span>
      </label>
      <label class="field">
        <span>Working Directory</span>
        <DirectoryPicker v-model="selectedSettingsChat.cwd" empty-label="Profile home" clear-title="Leave chat cwd empty" />
      </label>
      <label class="field">
        <span>Chat Prompt</span>
        <textarea v-model="selectedSettingsChat.common_prompt" class="form-control form-control-sm" rows="5"></textarea>
      </label>
      <div class="field">
        <span>Roles</span>
        <div v-if="!props.roles.length" class="empty-panel">No roles</div>
        <label v-for="role in props.roles" :key="role.id" class="role-check">
          <input
            class="form-check-input"
            :type="selectedSettingsChat.type === 'direct' ? 'radio' : 'checkbox'"
            :name="`chat-role-${selectedSettingsChat.id}`"
            :checked="selectedSettingsChat.member_role_ids.includes(role.id)"
            @change="toggleRole(selectedSettingsChat, role.id)"
          />
          <span>{{ role.name }}</span>
        </label>
      </div>
      <div class="settings-actions">
        <button class="btn btn-sm btn-outline-danger settings-delete" type="button" @click="emit('delete-chat', selectedSettingsChat)">
          <i class="bi bi-trash"></i>
          <span>Delete Chat</span>
        </button>
        <button class="btn btn-sm btn-primary settings-save" type="submit">
          <i class="bi bi-save"></i>
          <span>Save Settings</span>
        </button>
      </div>
    </form>
  </div>
</template>

<style scoped>
.list-section {
  padding: 3px 2px;
}

.chat-entry {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.chat-entry .sidebar-row {
  display: grid;
  gap: 2px;
  grid-template-columns: minmax(0, 1fr) 20px;
  min-width: 0;
  padding: 2px 3px;
}

.chat-entry .sidebar-row-main {
  overflow: hidden;
}

.chat-entry .sidebar-row-main > .bi {
  flex: 0 0 auto;
}

.chat-entry .sidebar-row-action {
  align-self: center;
  height: 20px;
  opacity: 0.66;
  width: 20px;
}

.chat-entry .sidebar-row-action .bi {
  font-size: 11px;
}

.dispatch-role-list {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
  padding: 0 2px 4px 24px;
}

.dispatch-role-chip {
  align-items: center;
  background: var(--color-canvas);
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  display: inline-flex;
  font-size: 11px;
  gap: 5px;
  min-height: 24px;
  min-width: 0;
  max-width: 100%;
  padding: 2px 7px;
}

.dispatch-role-chip.selected {
  background: var(--color-surface-selected);
  color: var(--color-text);
}

.dispatch-role-chip span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dispatch-role-empty {
  color: var(--color-text-muted);
  font-size: 11px;
  padding: 2px 0;
}

.chat-settings {
  border-top: 1px solid var(--color-border);
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 9px;
  min-height: 0;
  overflow: auto;
  padding: 10px;
}

.settings-title {
  align-items: center;
  color: var(--color-text);
  display: flex;
  font-size: 12px;
  font-weight: 700;
  justify-content: space-between;
}

.role-check {
  align-items: center;
  display: flex;
  gap: 7px;
  min-height: 28px;
}

.setting-check {
  align-items: center;
  display: flex;
  gap: 7px;
  min-height: 28px;
}

.setting-check span {
  color: var(--color-text);
  font-size: 12px;
}

.role-check span {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.settings-actions {
  display: flex;
  gap: 8px;
}

.settings-delete,
.settings-save {
  align-items: center;
  display: inline-flex;
  gap: 7px;
  justify-content: center;
}

.settings-save {
  flex: 1 1 auto;
}
</style>
