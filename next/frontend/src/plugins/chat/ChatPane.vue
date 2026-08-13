<script setup lang="ts">
import { computed, inject, onMounted, ref } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import { useLayoutStore } from "../../stores/layout";
import type { Chat, ChatList, ChatMessage, Role, Workspace } from "./types";
import { errorText } from "./types";

const injectedCtx = inject<PluginCtx>("pluginCtx"); if (injectedCtx === undefined) throw new Error("ChatPane requires PluginPaneHost"); const ctx: PluginCtx = injectedCtx;
const layout = useLayoutStore(); const messages = ref<ChatMessage[]>([]); const roles = ref<Role[]>([]); const chat = ref<Chat | null>(null); const draft = ref(""); const selected = ref<string[]>([]); const activeRoles = ref(new Set<string>()); const error = ref("");
const grouped = computed(() => { const result: Array<{ turn: string; sender: ChatMessage["sender"]; text: string; ts: number }> = []; for (const message of messages.value) { const prior = result.at(-1); if (prior !== undefined && prior.turn === message.turn_id && message.role === "assistant") prior.text += message.text; else result.push({ turn: message.turn_id, sender: message.sender, text: message.text, ts: message.created_at }); } return result; });
const members = computed(() => roles.value.filter((role) => chat.value?.member_role_ids.includes(role.id)));

async function load(): Promise<void> { const [list, workspace] = await Promise.all([ctx.bus.request("chat:_:chats:list", { chat_id: ctx.instanceId, include_messages: true }) as Promise<ChatList>, ctx.bus.request("chat:_:workspace:get", {}) as Promise<Workspace>]); chat.value = list.chats.find((item) => item.id === ctx.instanceId) ?? null; messages.value = list.messages ?? []; roles.value = workspace.roles; }
async function send(): Promise<void> { const message = draft.value.trim(); if (message === "") return; error.value = ""; try { const payload: Record<string, unknown> = { chat_id: ctx.instanceId, message }; if (selected.value.length > 0) payload.role_ids = selected.value; const result = await ctx.bus.request("chat:_:dispatch", payload) as { role_ids: string[] }; activeRoles.value = new Set([...activeRoles.value, ...result.role_ids]); draft.value = ""; } catch (cause) { error.value = errorText(cause); } }
async function stop(): Promise<void> { try { await ctx.bus.request("chat:_:stop", { chat_id: ctx.instanceId }); } catch (cause) { error.value = errorText(cause); } }
function open(type: string): void { layout.openInstance(type, "main"); }
onMounted(() => { ctx.bus.subscribe(`chat:${ctx.instanceId}:message`, (frame) => messages.value.push(frame.value as ChatMessage)); ctx.bus.subscribe(`chat:${ctx.instanceId}:turn-completed`, (frame) => { const value = frame.value as { role_id: string }; const next = new Set(activeRoles.value); next.delete(value.role_id); activeRoles.value = next; }); void load().catch((cause) => { error.value = errorText(cause); }); });
</script>

<template>
  <section class="chat-pane d-flex flex-column h-100">
    <header class="d-flex gap-1 align-items-center border-bottom px-2 py-1 small"><strong class="me-auto">{{ chat?.name ?? "Chat" }}</strong><button v-for="item in [['chats','bi-list','Chats'],['roles','bi-people','Roles'],['routes','bi-signpost-split','Routes']]" :key="item[0]" class="btn btn-sm btn-outline-secondary py-0" :title="item[2]" @click="open(item[0])"><i :class="item[1]" /></button></header>
    <div class="flex-grow-1 overflow-auto p-2" aria-live="polite">
      <article v-for="item in grouped" :key="item.turn + item.ts" class="message-box mb-2 p-2" :class="item.sender.from === 'user' ? 'user-box' : 'role-box'">
        <div class="small text-secondary mb-1">{{ item.sender.from === 'user' ? 'You' : item.sender.role_name }}</div><div class="message-text">{{ item.text }}</div>
      </article>
      <div v-if="activeRoles.size" class="small text-secondary"><span class="spinner-border spinner-border-sm me-1" />{{ activeRoles.size }} role turn(s) active</div>
    </div>
    <div class="border-top p-2"><div v-if="error" class="small text-danger mb-1">{{ error }}</div><div class="d-flex flex-wrap gap-2 mb-1 small"><label v-for="role in members" :key="role.id"><input v-model="selected" type="checkbox" :value="role.id" class="me-1">{{ role.name }}</label><span v-if="selected.length === 0" class="text-secondary">Auto route</span></div><div class="input-group"><textarea v-model="draft" class="form-control" rows="2" placeholder="Message" @keydown.ctrl.enter.prevent="send" /><button v-if="activeRoles.size" class="btn btn-outline-danger" title="Stop active turn" @click="stop"><i class="bi-stop-fill" /></button><button class="btn btn-primary" title="Dispatch (Ctrl+Enter)" @click="send"><i class="bi-send" /></button></div></div>
  </section>
</template>
<style scoped>.message-box{background:var(--bs-tertiary-bg);border-radius:2px;white-space:pre-wrap}.user-box{border-inline-start:3px solid var(--bs-primary)}.role-box{border-inline-start:3px solid var(--bs-success)}.message-text{font-size:13px}.chat-pane textarea{resize:none}</style>
