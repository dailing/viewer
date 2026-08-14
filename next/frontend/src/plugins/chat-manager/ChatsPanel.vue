<script setup lang="ts">
import { computed, inject, onMounted, ref } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { useLayoutStore } from "../../stores/layout";
import type { Chat, ChatList, Role } from "../chat/types";
import { errorText } from "../chat/types";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("ChatsPanel requires PluginPaneHost");
const ctx = injectedCtx;
const layout = useLayoutStore();
const chats = ref<Chat[]>([]);
const roles = ref<Role[]>([]);
const active = ref("");
const selectedID = ref("");
const name = ref("New Chat");
const root = ref("");
const type = ref("group");
const error = ref("");
const selected = computed(() => chats.value.find((chat) => chat.id === selectedID.value) ?? null);

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
    const chat = await ctx.bus.request("chat:_:chats:create", { name: name.value, root: root.value, type: type.value, member_role_ids: [] }) as Chat;
    await load();
    selectedID.value = chat.id;
  } catch (cause) { error.value = errorText(cause); }
}

async function patch(chat: Chat, values: Record<string, unknown>): Promise<void> {
  error.value = "";
  try {
    await ctx.bus.request("chat:_:chats:patch", { id: chat.id, ...values });
    await load();
  } catch (cause) { error.value = errorText(cause); }
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
  } catch (cause) { error.value = errorText(cause); }
}

onMounted(() => void load().catch((cause) => { error.value = errorText(cause); }));
</script>

<template>
  <section class="h-100 overflow-auto p-2">
    <div v-if="error" class="small text-danger mb-2">{{ error }}</div>
    <div class="row g-1 mb-3">
      <div class="col-md-3"><input v-model="name" class="form-control form-control-sm" placeholder="名字"></div>
      <div class="col-md-5"><input v-model="root" class="form-control form-control-sm" placeholder="Root（绝对路径）"></div>
      <div class="col-md-2"><select v-model="type" class="form-select form-select-sm"><option value="group">group</option><option value="direct">direct</option></select></div>
      <div class="col-md-2"><button class="btn btn-sm btn-primary w-100" @click="create"><i class="bi-plus-lg me-1"></i>新建</button></div>
    </div>
    <div class="row g-2">
      <div class="col-md-4">
        <div v-for="chat in chats" :key="chat.id" class="d-flex align-items-center gap-1 p-1 mb-1" :class="chat.id === selectedID ? 'bg-body-tertiary' : ''">
          <button class="btn btn-sm text-start flex-grow-1" @click="selectedID = chat.id">
            {{ chat.pinned ? "★ " : "" }}{{ chat.name }}<small class="d-block text-secondary text-truncate">{{ chat.root }}</small>
          </button>
          <button class="btn btn-sm" :title="chat.pinned ? 'Unpin' : 'Pin'" @click="patch(chat, { pinned: !chat.pinned })"><i class="bi" :class="chat.pinned ? 'bi-pin-angle-fill' : 'bi-pin-angle'"></i></button>
          <button class="btn btn-sm text-danger" title="Delete" @click="remove(chat)"><i class="bi-trash"></i></button>
        </div>
      </div>
      <div v-if="selected" class="col-md-8">
        <div class="row g-1">
          <div class="col-8"><input v-model="selected.name" class="form-control form-control-sm" placeholder="名字"></div>
          <div class="col-4"><select v-model="selected.type" class="form-select form-select-sm"><option value="group">group</option><option value="direct">direct</option></select></div>
          <div class="col-12"><input v-model="selected.root" class="form-control form-control-sm" placeholder="Root"></div>
          <div class="col-12"><textarea v-model="selected.common_prompt" class="form-control form-control-sm" rows="4" placeholder="Chat common prompt"></textarea></div>
          <div class="col-12 small text-secondary mt-2">Member Roles</div>
          <div class="col-12 d-flex flex-wrap gap-3">
            <label v-for="role in roles" :key="role.id" class="small"><input v-model="selected.member_role_ids" type="checkbox" :value="role.id" class="me-1">{{ role.name }}</label>
            <span v-if="roles.length === 0" class="small text-secondary">尚无 Role</span>
          </div>
        </div>
        <div class="d-flex gap-1 mt-3">
          <button class="btn btn-sm btn-primary" @click="saveSelected">保存</button>
          <button class="btn btn-sm btn-outline-secondary" @click="activate(selected)">{{ selected.id === active ? "打开" : "激活并打开" }}</button>
        </div>
      </div>
    </div>
  </section>
</template>
