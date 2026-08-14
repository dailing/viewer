<script setup lang="ts">
import { computed, inject, onMounted, ref } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { useLayoutStore } from "../../stores/layout";
import type { Chat, ChatList, Role } from "../chat/types";
import { errorText } from "../chat/types";
import MasterDetail from "./MasterDetail.vue";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("ChatsPanel requires PluginPaneHost");
const ctx = injectedCtx;
const layout = useLayoutStore();
const chats = ref<Chat[]>([]);
const roles = ref<Role[]>([]);
const active = ref("");
const selectedID = ref("");
const error = ref("");
const selected = computed(() => chats.value.find((chat) => chat.id === selectedID.value) ?? null);
const items = computed(() => chats.value.map(({ id, name }) => ({ id, name })));

function notifyChatsChanged(): void {
  window.dispatchEvent(new CustomEvent("viewer:chats-changed"));
}

async function load(): Promise<void> {
  const [list, loadedRoles] = await Promise.all([
    ctx.bus.request("chat:_:chats:list", {}) as Promise<ChatList>,
    ctx.bus.request("chat:_:roles:list", {}) as Promise<Role[]>,
  ]);
  chats.value = list.chats;
  roles.value = loadedRoles;
  active.value = list.active_chat_id;
  if (!chats.value.some((chat) => chat.id === selectedID.value)) selectedID.value = chats.value[0]?.id ?? "";
}

async function create(): Promise<void> {
  error.value = "";
  try {
    const chat = await ctx.bus.request("chat:_:chats:create", {
      name: "New Chat",
      root: ".",
      type: "group",
      member_role_ids: [],
    }) as Chat;
    await load();
    selectedID.value = chat.id;
    notifyChatsChanged();
  } catch (cause) {
    error.value = errorText(cause);
  }
}

async function patch(chat: Chat, values: Record<string, unknown>): Promise<void> {
  error.value = "";
  try {
    await ctx.bus.request("chat:_:chats:patch", { id: chat.id, ...values });
    await load();
    notifyChatsChanged();
  } catch (cause) {
    error.value = errorText(cause);
  }
}

async function saveSelected(): Promise<void> {
  if (selected.value === null) return;
  await patch(selected.value, {
    name: selected.value.name,
    root: selected.value.root,
    type: selected.value.type,
    common_prompt: selected.value.common_prompt,
    member_role_ids: selected.value.member_role_ids,
  });
}

async function activate(chat: Chat): Promise<void> {
  await ctx.bus.request("chat:_:chats:activate", { id: chat.id });
  active.value = chat.id;
  layout.openInstance("chat", chat.id);
}

async function remove(chat: Chat): Promise<void> {
  if (!confirm(`Delete ${chat.name}?`)) return;
  try {
    await ctx.bus.request("chat:_:chats:delete", { id: chat.id });
    await load();
    notifyChatsChanged();
  } catch (cause) {
    error.value = errorText(cause);
  }
}

onMounted(() => void load().catch((cause) => { error.value = errorText(cause); }));
</script>

<template>
  <MasterDetail :items="items" :selected-id="selectedID" create-label="＋ 新建聊天" @select="selectedID = $event" @create="create">
    <template #detail>
      <div v-if="error" class="small text-danger mb-2">{{ error }}</div>
      <div v-if="selected" class="chat-config">
        <div class="row g-2">
          <label class="col-md-8 form-label small">Name<input v-model="selected.name" class="form-control form-control-sm mt-1"></label>
          <label class="col-md-4 form-label small">Type<select v-model="selected.type" class="form-select form-select-sm mt-1"><option value="group">group</option><option value="direct">direct</option></select></label>
          <label class="col-12 form-label small">Root<input v-model="selected.root" class="form-control form-control-sm mt-1"></label>
          <label class="col-12 form-label small">Common prompt<textarea v-model="selected.common_prompt" class="form-control form-control-sm mt-1" rows="5"></textarea></label>
          <div class="col-12 small text-secondary">Member Roles</div>
          <div class="col-12 d-flex flex-wrap gap-3">
            <label v-for="role in roles" :key="role.id" class="small"><input v-model="selected.member_role_ids" type="checkbox" :value="role.id" class="me-1">{{ role.name }}</label>
            <span v-if="roles.length === 0" class="small text-secondary">尚无 Role</span>
          </div>
        </div>
        <div class="d-flex align-items-center gap-1 mt-3">
          <button class="btn btn-sm btn-primary" @click="saveSelected">保存</button>
          <button class="btn btn-sm btn-outline-secondary" @click="activate(selected)">{{ selected.id === active ? "打开" : "激活并打开" }}</button>
          <button class="btn btn-sm btn-outline-secondary" :title="selected.pinned ? 'Unpin' : 'Pin'" :aria-label="selected.pinned ? 'Unpin' : 'Pin'" @click="patch(selected, { pinned: !selected.pinned })"><i class="bi" :class="selected.pinned ? 'bi-pin-angle-fill' : 'bi-pin-angle'"></i></button>
          <button class="btn btn-sm btn-outline-danger ms-auto" @click="remove(selected)"><i class="bi bi-trash me-1"></i>删除</button>
        </div>
      </div>
      <div v-else-if="!error" class="small text-secondary">选择聊天以编辑配置。</div>
    </template>
  </MasterDetail>
</template>
