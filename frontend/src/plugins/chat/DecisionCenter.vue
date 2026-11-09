<script setup lang="ts">
import { inject, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import DecisionCard from "./DecisionCard.vue";
import { decisionResponsePayload, mergeDecision, type DecisionResponseIntent } from "./decisionInput";
import { storeTaskReadingAnchor } from "./historyWindow";
import { ensureTaskView } from "./viewInstances";
import type { Decision, DecisionList } from "./types";
import { errorText } from "./types";

const injected = inject<PluginCtx>("pluginCtx");
if (!injected) throw new Error("DecisionCenter requires PluginPaneHost");
const ctx = injected;
const tab = ref<"pending" | "history">("pending");
const decisions = ref<Decision[]>([]);
const drafts = ref<Record<string, string>>({});
const busy = ref<Record<string, boolean>>({});
const loading = ref(false);
const hasMore = ref(false);
const error = ref("");
let cursor: { createdAt: number; id: string } | undefined;

function visibleInTab(decision: Decision): boolean {
  return tab.value === "pending" ? decision.status === "pending" || decision.status === "dispatching" : decision.status === "resolved" || decision.status === "cancelled" || decision.status === "invalid";
}
async function load(append = false): Promise<void> {
  loading.value = true; error.value = "";
  try {
    const request: Record<string, unknown> = { limit: 50 };
    if (tab.value === "pending") request.pending = true; else request.history = true;
    if (append && cursor) { request.before_created_at = cursor.createdAt; request.before_id = cursor.id; }
    const result = await ctx.bus.request("chat:_:decisions:list", request) as DecisionList;
    const byId = new Map((append ? [...decisions.value, ...result.decisions] : result.decisions).map((item) => [item.id, item]));
    decisions.value = [...byId.values()];
    hasMore.value = result.has_more === true;
    cursor = result.next_created_at && result.next_id ? { createdAt: result.next_created_at, id: result.next_id } : undefined;
  } catch (cause) { error.value = errorText(cause); }
  finally { loading.value = false; }
}
function onDecision(frame: { value?: unknown }): void {
  const decision = frame.value as Decision | undefined;
  if (!decision?.id) return;
  if (visibleInTab(decision)) decisions.value = mergeDecision(decisions.value, decision);
  else decisions.value = decisions.value.filter((item) => item.id !== decision.id);
}
async function respond(intent: DecisionResponseIntent): Promise<void> {
  busy.value[intent.decisionId] = true; error.value = "";
  try {
    const result = await ctx.bus.request("chat:_:decisions:respond", decisionResponsePayload(intent), { timeout: 90_000 }) as { decision?: Decision };
    drafts.value[intent.decisionId] = "";
    if (result.decision) onDecision({ value: result.decision });
  } catch (cause) { error.value = errorText(cause); }
  finally { busy.value[intent.decisionId] = false; }
}
async function cancel(decision: Decision): Promise<void> {
  busy.value[decision.id] = true; error.value = "";
  try {
    const result = await ctx.bus.request("chat:_:decisions:cancel", { decision_id: decision.id, expected_version: decision.version }) as { decision: Decision };
    onDecision({ value: result.decision });
  } catch (cause) { error.value = errorText(cause); }
  finally { busy.value[decision.id] = false; }
}
async function openSource(decision: Decision): Promise<void> {
  try {
    const target = await ctx.bus.request("chat:_:decisions:open", { decision_id: decision.id }) as { created_at: number; source_message_id: string };
    const viewKey = ensureTaskView(decision.chat_id, decision.task_id);
    const anchor = { createdAt: target.created_at, messageId: target.source_message_id };
    storeTaskReadingAnchor(viewKey, decision.task_id, anchor);
    await ctx.shell.open({ paneType: "chat", instanceId: viewKey });
    window.dispatchEvent(new CustomEvent("viewer:task-open-source", { detail: { viewKey, anchor } }));
  } catch (cause) { error.value = errorText(cause); }
}
watch(tab, () => { cursor = undefined; void load(); });
onMounted(() => { ctx.setChrome({ title: "Messages / Decisions" }); ctx.bus.subscribe("chat:_:decision", onDecision); void load(); });
onBeforeUnmount(() => ctx.bus.unsubscribe("chat:_:decision", onDecision));
</script>

<template>
  <div class="decision-center">
    <header class="decision-tabs"><button type="button" :class="{ active: tab === 'pending' }" @click="tab = 'pending'">待处理</button><button type="button" :class="{ active: tab === 'history' }" @click="tab = 'history'">历史</button><button class="refresh" type="button" title="刷新" @click="load()"><i class="bi bi-arrow-clockwise" /></button></header>
    <div v-if="error" class="decision-error">{{ error }}</div>
    <main class="decision-list">
      <DecisionCard v-for="decision in decisions" :key="decision.id" :decision="decision" :draft="drafts[decision.id] ?? ''" :busy="busy[decision.id]" @update:draft="drafts[decision.id] = $event" @respond="respond" @cancel="cancel(decision)" @open="openSource(decision)" />
      <div v-if="!loading && decisions.length === 0" class="decision-empty">{{ tab === 'pending' ? '没有待处理的 Decision。' : '还没有 Decision 历史。' }}</div>
      <button v-if="hasMore" class="load-more" type="button" :disabled="loading" @click="load(true)">加载更多</button>
    </main>
  </div>
</template>

<style scoped>
.decision-center{display:flex;flex-direction:column;height:100%;min-height:0;color:var(--color-text)}.decision-tabs{display:flex;flex:0 0 auto;gap:4px;padding:6px 8px;border-bottom:1px solid var(--color-border)}.decision-tabs button,.load-more{background:transparent;border:0;border-radius:var(--radius-sm);color:var(--color-text-muted);cursor:pointer;padding:4px 8px}.decision-tabs button.active{background:var(--color-surface-hover);color:var(--color-text)}.decision-tabs .refresh{margin-left:auto}.decision-error{color:var(--bs-danger);font-size:var(--font-size-ui-small);padding:6px 10px}.decision-list{flex:1;min-height:0;overflow:auto;padding:6px 10px}.decision-empty{color:var(--color-text-muted);padding:20px;text-align:center}.load-more{display:block;margin:10px auto}
</style>
