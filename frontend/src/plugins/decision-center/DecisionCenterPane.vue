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
type Cursor = { createdAt: number; id: string };
const PAGE_SIZE = 50;
const entries = reactive<Record<Section, Decision[]>>({ pending: [], history: [] });
const loading = reactive<Record<Section, boolean>>({ pending: false, history: false });
const hasMore = reactive<Record<Section, boolean>>({ pending: false, history: false });
const pages = reactive<Record<Section, number>>({ pending: 1, history: 1 });
const pageCursors: Record<Section, Array<Cursor | undefined>> = { pending: [undefined], history: [undefined] };
const generations: Record<Section, number> = { pending: 0, history: 0 };
const pendingCount = ref(0);
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
async function load(section: Section, pageIndex = pages[section] - 1): Promise<void> {
  const cursor = pageCursors[section][pageIndex];
  if (pageIndex < 0 || (pageIndex > 0 && !cursor)) return;
  const generation = ++generations[section];
  loading[section] = true; error.value = "";
  try {
    const request: Record<string, unknown> = { limit: PAGE_SIZE };
    if (section === "pending") {
      request.pending = true;
      request.include_pending_count = true;
    } else {
      request.history = true;
    }
    if (cursor) { request.before_created_at = cursor.createdAt; request.before_id = cursor.id; }
    const result = await ctx.bus.request("chat:_:decisions:list", request) as DecisionList;
    if (generation !== generations[section]) return;
    entries[section] = sort(result.decisions ?? []);
    hasMore[section] = result.has_more === true;
    const next = result.next_created_at && result.next_id ? { createdAt: result.next_created_at, id: result.next_id } : undefined;
    pageCursors[section].length = pageIndex + 1;
    if (next) pageCursors[section][pageIndex + 1] = next;
    pages[section] = pageIndex + 1;
    if (section === "pending") pendingCount.value = result.pending_count ?? 0;
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
      <span v-if="pendingCount > 0" class="pending-count">{{ pendingCount }} 待处理</span>
      <button type="button" title="刷新" @click="refresh"><i class="bi bi-arrow-clockwise" /></button>
    </header>
    <div v-if="error" class="decision-error">{{ error }}</div>
    <main class="decision-list">
      <section v-if="entries.pending.length > 0 || loading.pending" class="decision-section">
        <h2>待处理</h2>
        <DecisionEntry v-for="decision in entries.pending" :key="decision.id" :decision="decision" :draft="drafts[decision.id] ?? ''" :busy="busy[decision.id]" @update:draft="drafts[decision.id] = $event" @respond="respond" @cancel="cancel(decision)" @open="openSource(decision)" />
        <nav v-if="pages.pending > 1 || hasMore.pending" class="decision-pagination" aria-label="待处理分页">
          <button type="button" :disabled="pages.pending === 1 || loading.pending" @click="load('pending', pages.pending - 2)"><i class="bi bi-chevron-left" /> 上一页</button>
          <span>第 {{ pages.pending }} 页</span>
          <button type="button" :disabled="!hasMore.pending || loading.pending" @click="load('pending', pages.pending)">下一页 <i class="bi bi-chevron-right" /></button>
        </nav>
      </section>
      <section v-if="entries.history.length > 0 || loading.history" class="decision-section history-section">
        <h2>历史</h2>
        <DecisionEntry v-for="decision in entries.history" :key="decision.id" :decision="decision" :draft="drafts[decision.id] ?? ''" :busy="busy[decision.id]" @update:draft="drafts[decision.id] = $event" @respond="respond" @cancel="cancel(decision)" @open="openSource(decision)" />
        <nav v-if="pages.history > 1 || hasMore.history" class="decision-pagination" aria-label="历史分页">
          <button type="button" :disabled="pages.history === 1 || loading.history" @click="load('history', pages.history - 2)"><i class="bi bi-chevron-left" /> 上一页</button>
          <span>第 {{ pages.history }} 页</span>
          <button type="button" :disabled="!hasMore.history || loading.history" @click="load('history', pages.history)">下一页 <i class="bi bi-chevron-right" /></button>
        </nav>
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
.decision-header button,.decision-pagination button { background: transparent; border: 0; border-radius: var(--radius-sm); color: var(--color-text-muted); cursor: pointer; padding: 4px 8px; }
.decision-header button { margin-left: auto; }
.decision-header button:hover,.decision-pagination button:hover:not(:disabled) { background: var(--color-surface-hover); color: var(--color-text); }
.decision-pagination button:disabled { cursor: default; opacity: .4; }
.decision-error { color: var(--bs-danger); font-size: var(--font-size-ui-small); padding: 6px 10px; }
.decision-list { flex: 1; min-height: 0; overflow: auto; padding: 6px 10px; }
.decision-section h2 { color: var(--color-text-muted); font-size: var(--font-size-ui-small); font-weight: 600; margin: 6px 0 2px; }
.history-section { margin-top: 14px; }
.decision-empty { color: var(--color-text-muted); padding: 20px; text-align: center; }
.decision-pagination { align-items: center; display: flex; font-size: var(--font-size-ui-small); gap: 8px; justify-content: center; padding: 8px 0 2px; }
.decision-pagination span { color: var(--color-text-muted); }
</style>
