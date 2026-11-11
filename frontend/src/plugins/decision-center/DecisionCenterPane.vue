<script setup lang="ts">
import { inject, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import DecisionEntry from "./DecisionEntry.vue";
import { decisionResponsePayload, mergeDecision, type DecisionResponseIntent } from "../chat/decisionInput";
import { storeTaskReadingAnchor } from "../chat/historyWindow";
import { ensureTaskView } from "../chat/viewInstances";
import type { Decision, DecisionList } from "../chat/types";
import { errorText } from "../chat/types";
import { registryHasChat } from "./runtime";

const injected = inject<PluginCtx>("pluginCtx");
if (!injected) throw new Error("DecisionCenter requires PluginPaneHost");
const ctx = injected;
type Section = "pending" | "history";
const entries = reactive<Record<Section, Decision[]>>({ pending: [], history: [] });
const loading = reactive<Record<Section, boolean>>({ pending: false, history: false });
const hasMore = reactive<Record<Section, boolean>>({ pending: false, history: false });
const cursors: Record<Section, { createdAt: number; id: string } | undefined> = { pending: undefined, history: undefined };
const generations: Record<Section, number> = { pending: 0, history: 0 };
const drafts = ref<Record<string, string>>({});
const busy = reactive<Record<string, boolean>>({});
const error = ref("");
let refreshTimer: ReturnType<typeof setTimeout> | undefined;

function sectionFor(decision: Decision): Section {
  return decision.status === "pending" || decision.status === "dispatching" ? "pending" : "history";
}
function sort(values: Decision[]): Decision[] {
  return values.sort((a, b) => b.created_at - a.created_at || a.id.localeCompare(b.id));
}
async function load(section: Section, append = false): Promise<void> {
  const generation = ++generations[section];
  loading[section] = true; error.value = "";
  try {
    const request: Record<string, unknown> = { limit: 50 };
    if (section === "pending") request.pending = true; else request.history = true;
    const cursor = cursors[section];
    if (append && cursor) { request.before_created_at = cursor.createdAt; request.before_id = cursor.id; }
    const result = await ctx.bus.request("chat:_:decisions:list", request) as DecisionList;
    if (generation !== generations[section]) return;
    const byId = new Map((append ? [...entries[section], ...result.decisions] : result.decisions).map((item) => [item.id, item]));
    entries[section] = sort([...byId.values()]);
    hasMore[section] = result.has_more === true;
    cursors[section] = result.next_created_at && result.next_id ? { createdAt: result.next_created_at, id: result.next_id } : undefined;
  } catch (cause) {
    if (generation === generations[section]) error.value = errorText(cause);
  } finally {
    if (generation === generations[section]) loading[section] = false;
  }
}
function refresh(): void {
  void Promise.all([load("pending"), load("history")]);
}
function scheduleRefresh(): void {
  if (refreshTimer !== undefined) clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => { refreshTimer = undefined; refresh(); }, 150);
}
function onDecision(frame: { value?: unknown }): void {
  const decision = frame.value as Decision | undefined;
  if (!decision?.id) return;
  const target = sectionFor(decision);
  const other: Section = target === "pending" ? "history" : "pending";
  entries[other] = entries[other].filter((item) => item.id !== decision.id);
  entries[target] = sort(mergeDecision(entries[target], decision));
  scheduleRefresh();
}
async function respond(intent: DecisionResponseIntent): Promise<void> {
  busy[intent.decisionId] = true; error.value = "";
  try {
    const result = await ctx.bus.request("chat:_:decisions:respond", decisionResponsePayload(intent), { timeout: 90_000 }) as { decision?: Decision };
    drafts.value[intent.decisionId] = "";
    if (result.decision) onDecision({ value: result.decision });
  } catch (cause) { error.value = errorText(cause); }
  finally { busy[intent.decisionId] = false; }
}
async function cancel(decision: Decision): Promise<void> {
  busy[decision.id] = true; error.value = "";
  try {
    const result = await ctx.bus.request("chat:_:decisions:cancel", { decision_id: decision.id, expected_version: decision.version }) as { decision: Decision };
    onDecision({ value: result.decision });
  } catch (cause) { error.value = errorText(cause); }
  finally { busy[decision.id] = false; }
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
function onWindowFocus(): void { refresh(); }
function onPluginRegistry(frame: { value?: unknown }): void {
  if (registryHasChat(frame.value)) scheduleRefresh();
}
onMounted(() => {
  ctx.setChrome({ title: "Decisions" });
  ctx.bus.subscribe("chat:_:decision", onDecision);
  ctx.bus.subscribe("plugins:_:list", onPluginRegistry);
  window.addEventListener("focus", onWindowFocus);
  refresh();
});
onBeforeUnmount(() => {
  if (refreshTimer !== undefined) clearTimeout(refreshTimer);
  ctx.bus.unsubscribe("chat:_:decision", onDecision);
  ctx.bus.unsubscribe("plugins:_:list", onPluginRegistry);
  window.removeEventListener("focus", onWindowFocus);
});
</script>

<template>
  <div class="decision-center">
    <header class="decision-header">
      <strong>Decisions</strong>
      <span v-if="entries.pending.length > 0" class="pending-count">{{ entries.pending.length }} 待处理</span>
      <button type="button" title="刷新" @click="refresh"><i class="bi bi-arrow-clockwise" /></button>
    </header>
    <div v-if="error" class="decision-error">{{ error }}</div>
    <main class="decision-list">
      <section v-if="entries.pending.length > 0 || loading.pending" class="decision-section">
        <h2>待处理</h2>
        <DecisionEntry v-for="decision in entries.pending" :key="decision.id" :decision="decision" :draft="drafts[decision.id] ?? ''" :busy="busy[decision.id]" @update:draft="drafts[decision.id] = $event" @respond="respond" @cancel="cancel(decision)" @open="openSource(decision)" />
        <button v-if="hasMore.pending" class="load-more" type="button" :disabled="loading.pending" @click="load('pending', true)">加载更多待处理</button>
      </section>
      <section v-if="entries.history.length > 0 || loading.history" class="decision-section history-section">
        <h2>历史</h2>
        <DecisionEntry v-for="decision in entries.history" :key="decision.id" :decision="decision" :draft="drafts[decision.id] ?? ''" :busy="busy[decision.id]" @update:draft="drafts[decision.id] = $event" @respond="respond" @cancel="cancel(decision)" @open="openSource(decision)" />
        <button v-if="hasMore.history" class="load-more" type="button" :disabled="loading.history" @click="load('history', true)">加载更多历史</button>
      </section>
      <div v-if="!loading.pending && !loading.history && entries.pending.length === 0 && entries.history.length === 0" class="decision-empty">没有 Decision。</div>
    </main>
  </div>
</template>

<style scoped>
.decision-center { color: var(--color-text); display: flex; flex-direction: column; height: 100%; min-height: 0; }
.decision-header { align-items: center; border-bottom: 1px solid var(--color-border); display: flex; flex: 0 0 auto; gap: 8px; padding: 6px 10px; }
.decision-header strong { font-size: var(--font-size-ui); }
.pending-count { color: var(--bs-primary); font-size: var(--font-size-ui-small); }
.decision-header button,.load-more { background: transparent; border: 0; border-radius: var(--radius-sm); color: var(--color-text-muted); cursor: pointer; padding: 4px 8px; }
.decision-header button { margin-left: auto; }
.decision-header button:hover,.load-more:hover { background: var(--color-surface-hover); color: var(--color-text); }
.decision-error { color: var(--bs-danger); font-size: var(--font-size-ui-small); padding: 6px 10px; }
.decision-list { flex: 1; min-height: 0; overflow: auto; padding: 6px 10px; }
.decision-section h2 { color: var(--color-text-muted); font-size: var(--font-size-ui-small); font-weight: 600; margin: 6px 0 2px; }
.history-section { margin-top: 14px; }
.decision-empty { color: var(--color-text-muted); padding: 20px; text-align: center; }
.load-more { display: block; margin: 8px auto; }
</style>
