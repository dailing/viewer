<script setup lang="ts">
import { computed, inject, onMounted, ref, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import TrendChart from "./TrendChart.vue";
import { dateRange, formatCompact, formatDuration, localDateKey } from "./metricsFormat";

interface ChatOption { id: string; name: string }
interface TaskOption { id: string; chat_id: string; title: string; chat_name?: string }
interface MetricDay {
  date: string; tasks?: number; prompts?: number; turns?: number; decisions?: number; tool_calls?: number;
  calls?: number; attempts?: number; input_tokens: number; output_tokens: number; total_tokens: number;
}
interface TaskRank { task_id: string; chat_id?: string; title?: string; prompts?: number; turns?: number; decisions?: number; tool_calls?: number; attempts?: number; total_tokens: number }
interface TurnRank { turn_id: string; task_id: string; task_title: string; role_name: string; provider: string; model: string; started_at: number; duration_ms: number; total_tokens: number; tool_calls: number }
interface NamedRank { name: string; turns?: number; calls?: number; attempts?: number; total_tokens?: number }
interface Dashboard {
  available?: boolean; from: number; to: number; timezone: string; summary: Record<string, number>; daily: MetricDay[];
  top_tasks?: TaskRank[]; top_turns?: TurnRank[]; top_roles?: NamedRank[]; top_tools?: NamedRank[];
  by_purpose?: NamedRank[]; by_model?: NamedRank[];
}

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (!injectedCtx) throw new Error("MetricsPane must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;
const ledger = ref(ctx.storage.get("ledger", "agent"));
const chats = ref<ChatOption[]>([]);
const tasks = ref<TaskOption[]>([]);
const chatID = ref(ctx.storage.get("chatID", ""));
const taskID = ref(ctx.storage.get("taskID", ""));
const initialRange = dateRange(30);
const fromDate = ref(ctx.storage.get("fromDate", initialRange.from));
const toDate = ref(ctx.storage.get("toDate", initialRange.to));
const granularity = ref(ctx.storage.get("granularity", "day"));
const agent = ref<Dashboard | null>(null);
const llm = ref<Dashboard | null>(null);
const loading = ref(false);
const error = ref("");
let generation = 0;
const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;

const current = computed(() => ledger.value === "agent" ? agent.value : llm.value);
const daily = computed(() => current.value?.daily ?? []);
const numericKeys: Array<Exclude<keyof MetricDay, "date">> = ["tasks", "prompts", "turns", "decisions", "tool_calls", "calls", "attempts", "input_tokens", "output_tokens", "total_tokens"];
const groupedDaily = computed<MetricDay[]>(() => {
  if (granularity.value === "day") return daily.value;
  const groups = new Map<string, MetricDay>();
  for (const point of daily.value) {
    const date = new Date(`${point.date}T12:00:00`);
    let key: string;
    if (granularity.value === "week") {
      const weekday = (date.getDay() + 6) % 7;
      date.setDate(date.getDate() - weekday);
      key = localDateKey(date);
    } else key = point.date.slice(0, 7);
    const row = groups.get(key) ?? { date: key, input_tokens: 0, output_tokens: 0, total_tokens: 0 };
    for (const name of numericKeys) {
      if (point[name] !== undefined) row[name] = (row[name] ?? 0) + Number(point[name]);
    }
    groups.set(key, row);
  }
  return [...groups.values()];
});
const tokenSeries = computed(() => [
  { name: "Input", color: "#58749a", values: groupedDaily.value.map((point) => point.input_tokens) },
  { name: "Output", color: "#4f765f", values: groupedDaily.value.map((point) => point.output_tokens) },
  { name: "Total", color: "#8a7047", values: groupedDaily.value.map((point) => point.total_tokens) },
]);
const heatmap = computed(() => {
  const values = daily.value;
  if (!values.length) return [] as Array<MetricDay | null>;
  const first = new Date(`${values[0].date}T12:00:00`);
  const blanks = Array.from({ length: (first.getDay() + 6) % 7 }, () => null);
  return [...blanks, ...values];
});
const heatMaximum = computed(() => Math.max(1, ...daily.value.map((point) => point.total_tokens || Number(point.prompts ?? 0) + Number(point.turns ?? 0) + Number(point.calls ?? 0))));
function heatLevel(point: MetricDay | null): number {
  if (!point) return -1;
  const value = point.total_tokens || Number(point.prompts ?? 0) + Number(point.turns ?? 0) + Number(point.calls ?? 0);
  if (value === 0) return 0;
  return Math.min(4, Math.max(1, Math.ceil(value / heatMaximum.value * 4)));
}
function summary(name: string): number { return Number(current.value?.summary?.[name] ?? 0); }
function percent(value: number): string { return `${value.toFixed(value > 0 && value < 10 ? 1 : 0)}%`; }
function taskTitle(id: string): string {
  return tasks.value.find((task) => task.id === id)?.title
    ?? agent.value?.top_tasks?.find((task) => task.task_id === id)?.title ?? id;
}
function selectTask(id: string): void {
  const task = tasks.value.find((item) => item.id === id);
  const ranked = agent.value?.top_tasks?.find((item) => item.task_id === id);
  const selectedChat = task?.chat_id ?? ranked?.chat_id;
  if (selectedChat && selectedChat !== chatID.value) chatID.value = selectedChat;
  taskID.value = id;
  void loadTasks().then(load);
}
function applyPreset(days: number): void {
  const range = dateRange(days);
  fromDate.value = range.from;
  toDate.value = range.to;
  void load();
}
async function loadTasks(): Promise<void> {
  try {
    const result = await ctx.bus.request("chat:_:tasks:list", { ...(chatID.value ? { chat_id: chatID.value } : {}), limit: 100 }) as { tasks: TaskOption[] };
    tasks.value = result.tasks;
    if (taskID.value && !tasks.value.some((task) => task.id === taskID.value)) taskID.value = "";
  } catch { tasks.value = []; }
}
async function load(): Promise<void> {
  const currentGeneration = ++generation;
  loading.value = true;
  error.value = "";
  ctx.storage.set("ledger", ledger.value); ctx.storage.set("chatID", chatID.value); ctx.storage.set("taskID", taskID.value);
  ctx.storage.set("fromDate", fromDate.value); ctx.storage.set("toDate", toDate.value); ctx.storage.set("granularity", granularity.value);
  const from = new Date(`${fromDate.value}T00:00:00`).getTime();
  const end = new Date(`${toDate.value}T00:00:00`); end.setDate(end.getDate() + 1);
  const payload = { from, to: end.getTime(), timezone, ...(chatID.value ? { chat_id: chatID.value } : {}), ...(taskID.value ? { task_id: taskID.value } : {}) };
  try {
    const [agentResult, llmResult] = await Promise.all([
      ctx.bus.request("chat:_:metrics:dashboard", payload) as Promise<Dashboard>,
      ctx.bus.request("llm:_:metrics:dashboard", payload) as Promise<Dashboard>,
    ]);
    if (currentGeneration !== generation) return;
    agent.value = agentResult;
    llm.value = llmResult;
  } catch (cause) {
    if (currentGeneration === generation) error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    if (currentGeneration === generation) loading.value = false;
  }
}
watch(chatID, () => { taskID.value = ""; void loadTasks().then(load); });
watch(ledger, () => ctx.storage.set("ledger", ledger.value));
onMounted(async () => {
  try { chats.value = ((await ctx.bus.request("chat:_:chats:list", {})) as { chats: ChatOption[] }).chats; } catch { chats.value = []; }
  await loadTasks();
  await load();
});
</script>

<template>
  <section class="metrics-pane">
    <header class="toolbar">
      <div class="tabs" role="tablist">
        <button :class="{ active: ledger === 'agent' }" @click="ledger = 'agent'">Agent Token</button>
        <button :class="{ active: ledger === 'llm' }" @click="ledger = 'llm'">Viewer LLM</button>
      </div>
      <button class="icon-button" title="刷新" :disabled="loading" @click="load"><i class="bi bi-arrow-clockwise" :class="{ spinning: loading }" /></button>
    </header>

    <div class="filters">
      <label>从 <input v-model="fromDate" type="date" @change="load"></label>
      <label>到 <input v-model="toDate" type="date" @change="load"></label>
      <div class="presets"><button @click="applyPreset(7)">7 天</button><button @click="applyPreset(30)">30 天</button><button @click="applyPreset(90)">90 天</button></div>
      <select v-model="granularity" aria-label="聚合粒度" @change="ctx.storage.set('granularity', granularity)"><option value="day">按日</option><option value="week">按周</option><option value="month">按月</option></select>
      <select v-model="chatID" aria-label="Chat"><option value="">全部 Chat</option><option v-for="chat in chats" :key="chat.id" :value="chat.id">{{ chat.name }}</option></select>
      <select v-model="taskID" aria-label="Task" @change="load"><option value="">全部 Task</option><option v-for="task in tasks" :key="task.id" :value="task.id">{{ task.chat_name ? `${task.chat_name} / ` : "" }}{{ task.title }}</option></select>
    </div>
    <div v-if="error" class="error"><i class="bi bi-exclamation-triangle" /> {{ error }}</div>

    <div v-if="current" class="content">
      <div class="summary-grid">
        <template v-if="ledger === 'agent'">
          <div><span>Tasks</span><strong>{{ formatCompact(summary('tasks')) }}</strong></div>
          <div><span>Prompts</span><strong>{{ formatCompact(summary('prompts')) }}</strong></div>
          <div><span>Turns</span><strong>{{ formatCompact(summary('turns')) }}</strong><small>{{ formatCompact(summary('failed_turns')) }} failed</small></div>
          <div><span>Decisions</span><strong>{{ formatCompact(summary('decisions')) }}</strong><small>{{ formatCompact(summary('pending_decisions')) }} pending</small></div>
          <div><span>工具调用</span><strong>{{ formatCompact(summary('tool_calls')) }}</strong></div>
          <div><span>Input</span><strong>{{ formatCompact(summary('input_tokens')) }}</strong><small>cache {{ formatCompact(summary('cached_input_tokens')) }}</small></div>
          <div><span>Output</span><strong>{{ formatCompact(summary('output_tokens')) }}</strong><small>reasoning {{ formatCompact(summary('reasoning_output_tokens')) }}</small></div>
          <div class="accent"><span>Total Tokens</span><strong>{{ formatCompact(summary('total_tokens')) }}</strong><small>{{ formatCompact(summary('usage_events')) }} 次真实上报</small></div>
          <div><span>平均 Turn</span><strong>{{ formatDuration(summary('average_turn_latency_ms')) }}</strong></div>
          <div><span>Token 覆盖率</span><strong>{{ percent(summary('usage_coverage_percent')) }}</strong><small>{{ formatCompact(summary('turns_with_usage')) }} / {{ formatCompact(summary('turns')) }} turns</small></div>
        </template>
        <template v-else>
          <div><span>LLM Calls</span><strong>{{ formatCompact(summary('calls')) }}</strong><small>{{ formatCompact(summary('succeeded_calls')) }} succeeded</small></div>
          <div><span>Attempts</span><strong>{{ formatCompact(summary('attempts')) }}</strong><small>含 fallback</small></div>
          <div><span>失败 Calls</span><strong>{{ formatCompact(summary('failed_calls')) }}</strong></div>
          <div><span>Input</span><strong>{{ formatCompact(summary('input_tokens')) }}</strong><small>cache {{ formatCompact(summary('cached_input_tokens')) }}</small></div>
          <div><span>Output</span><strong>{{ formatCompact(summary('output_tokens')) }}</strong><small>reasoning {{ formatCompact(summary('reasoning_output_tokens')) }}</small></div>
          <div class="accent"><span>Total Tokens</span><strong>{{ formatCompact(summary('total_tokens')) }}</strong><small>Viewer 自身调用</small></div>
          <div><span>平均 Attempt</span><strong>{{ formatDuration(summary('average_latency_ms')) }}</strong></div>
          <div><span>Usage 覆盖率</span><strong>{{ percent(summary('usage_coverage_percent')) }}</strong><small>{{ formatCompact(summary('usage_attempts')) }} / {{ formatCompact(summary('attempts')) }} attempts</small></div>
        </template>
      </div>

      <section class="panel">
        <div class="section-title"><h2>Token 趋势</h2><span>{{ granularity === 'day' ? '按日' : granularity === 'week' ? '按周' : '按月' }} · {{ current.timezone }}</span></div>
        <TrendChart :labels="groupedDaily.map(point => point.date)" :series="tokenSeries" />
      </section>

      <section class="panel">
        <div class="section-title"><h2>活动热力图</h2><span>颜色按每日 Token，缺少 Token 时按活动数</span></div>
        <div class="heatmap-wrap"><div class="weekdays"><span>一</span><span>三</span><span>五</span><span>日</span></div><div class="heatmap"><div v-for="(point, index) in heatmap" :key="point?.date ?? `blank-${index}`" class="heat-cell" :class="`level-${heatLevel(point)}`"><title v-if="point">{{ point.date }} · {{ formatCompact(point.total_tokens) }} tokens · {{ point.prompts ?? point.calls ?? 0 }} prompts/calls · {{ point.turns ?? point.attempts ?? 0 }} turns/attempts</title></div></div></div>
        <div class="heat-legend"><span>少</span><i class="level-0" /><i class="level-1" /><i class="level-2" /><i class="level-3" /><i class="level-4" /><span>多</span></div>
      </section>

      <div v-if="ledger === 'llm'" class="breakdowns">
        <section class="panel"><div class="section-title"><h2>按用途</h2></div><div v-for="row in current.by_purpose ?? []" :key="row.name" class="breakdown-row"><span>{{ row.name }}</span><span>{{ formatCompact(row.calls ?? 0) }} calls / {{ formatCompact(row.attempts ?? 0) }} attempts</span><strong>{{ formatCompact(row.total_tokens) }}</strong></div></section>
        <section class="panel"><div class="section-title"><h2>按模型</h2></div><div v-for="row in current.by_model ?? []" :key="row.name" class="breakdown-row"><span>{{ row.name }}</span><span>{{ formatCompact(row.attempts ?? 0) }} attempts</span><strong>{{ formatCompact(row.total_tokens) }}</strong></div></section>
      </div>

      <div class="rankings">
        <section class="panel"><div class="section-title"><h2>Task Top 10</h2><span>点击可筛选</span></div><div class="table-wrap"><table><thead><tr><th>#</th><th>Task</th><th>{{ ledger === 'agent' ? 'Turns' : 'Attempts' }}</th><th>Tokens</th></tr></thead><tbody><tr v-for="(row, index) in current.top_tasks ?? []" :key="row.task_id" @click="selectTask(row.task_id)"><td>{{ index + 1 }}</td><td class="name" :title="taskTitle(row.task_id)">{{ taskTitle(row.task_id) }}</td><td>{{ formatCompact(ledger === 'agent' ? row.turns : row.attempts) }}</td><td>{{ formatCompact(row.total_tokens) }}</td></tr></tbody></table></div></section>
        <section v-if="ledger === 'agent'" class="panel"><div class="section-title"><h2>Turn Top 10</h2></div><div class="table-wrap"><table><thead><tr><th>#</th><th>Turn / Role</th><th>工具</th><th>Tokens</th></tr></thead><tbody><tr v-for="(row, index) in current.top_turns ?? []" :key="row.turn_id" @click="selectTask(row.task_id)"><td>{{ index + 1 }}</td><td class="name"><span :title="row.task_title">{{ row.task_title }}</span><small>{{ row.role_name }} · {{ row.model || row.provider }}</small></td><td>{{ row.tool_calls }}</td><td>{{ formatCompact(row.total_tokens) }}</td></tr></tbody></table></div></section>
        <section v-if="ledger === 'agent'" class="panel"><div class="section-title"><h2>Role Top 10</h2></div><div class="table-wrap"><table><thead><tr><th>#</th><th>Role</th><th>Turns</th><th>Tokens</th></tr></thead><tbody><tr v-for="(row, index) in current.top_roles ?? []" :key="row.name"><td>{{ index + 1 }}</td><td class="name">{{ row.name }}</td><td>{{ row.turns }}</td><td>{{ formatCompact(row.total_tokens) }}</td></tr></tbody></table></div></section>
        <section v-if="ledger === 'agent'" class="panel"><div class="section-title"><h2>Tool Top 10</h2></div><div class="table-wrap"><table><thead><tr><th>#</th><th>Tool</th><th>Calls</th><th /></tr></thead><tbody><tr v-for="(row, index) in current.top_tools ?? []" :key="row.name"><td>{{ index + 1 }}</td><td class="name">{{ row.name }}</td><td>{{ formatCompact(row.calls) }}</td><td /></tr></tbody></table></div></section>
        <section v-else class="panel"><div class="section-title"><h2>账本边界</h2></div><p class="note">这里只统计 Viewer 的路由、标题、Summary、语音及 HTTP facade 等自身调用。Agent 内部上下文、工具循环和 Token 不会重复进入这套账。</p></section>
      </div>
      <div v-if="ledger === 'agent'" class="footnote">Cached input 与 reasoning output 是子集，不重复加到 Total。ACP context usage 不计为 Token 消耗。</div>
    </div>
    <div v-else-if="loading" class="empty">正在汇总真实账本…</div>
  </section>
</template>

<style scoped>
.metrics-pane { background: var(--color-canvas); color: var(--color-text); display: flex; flex-direction: column; height: 100%; min-height: 0; overflow: hidden; }
.toolbar { align-items: center; border-bottom: 1px solid var(--color-border); display: flex; flex: 0 0 auto; justify-content: space-between; padding: 8px 14px; }
.tabs { display: flex; gap: 4px; }
button, select, input { background: var(--color-canvas); border: 1px solid var(--color-border); border-radius: var(--radius-sm); color: var(--color-text); font: inherit; padding: 4px 8px; }
button { cursor: pointer; }
button:hover { background: var(--color-surface-hover); }
button.active { background: var(--color-surface-selected); border-color: var(--color-accent); color: var(--color-accent); }
button:disabled { cursor: default; opacity: .5; }
.icon-button { padding: 3px 7px; }
.filters { align-items: center; display: flex; flex: 0 0 auto; flex-wrap: wrap; gap: 7px; padding: 9px 14px; }
.filters label { align-items: center; color: var(--color-text-muted); display: flex; gap: 5px; }
.presets { display: flex; gap: 3px; }
.filters select { max-width: 210px; }
.content { display: flex; flex: 1; flex-direction: column; gap: 10px; min-height: 0; overflow: auto; padding: 0 14px 18px; }
.summary-grid { display: grid; gap: 1px; grid-template-columns: repeat(auto-fit, minmax(108px, 1fr)); }
.summary-grid > div { background: var(--color-surface-raised); display: flex; flex-direction: column; min-height: 65px; padding: 9px 10px; }
.summary-grid span { color: var(--color-text-muted); font-size: var(--font-size-ui-small); }
.summary-grid strong { font-size: calc(var(--font-size-ui) * 1.35); font-weight: 600; margin-top: 4px; }
.summary-grid small { color: var(--color-text-subtle); margin-top: auto; }
.summary-grid .accent { background: var(--color-surface-selected); }
.panel { border: 1px solid var(--color-border); min-width: 0; padding: 10px 12px; }
.section-title { align-items: baseline; display: flex; justify-content: space-between; margin-bottom: 6px; }
h2 { font-size: var(--font-size-ui); font-weight: 600; margin: 0; }
.section-title span, .footnote { color: var(--color-text-subtle); font-size: var(--font-size-ui-small); }
.heatmap-wrap { display: flex; gap: 7px; overflow-x: auto; padding: 4px 0 8px; }
.weekdays { color: var(--color-text-subtle); display: grid; flex: 0 0 12px; font-size: 9px; grid-template-rows: repeat(7, 11px); row-gap: 3px; }
.weekdays span:nth-child(1) { grid-row: 2; }.weekdays span:nth-child(2) { grid-row: 4; }.weekdays span:nth-child(3) { grid-row: 6; }.weekdays span:nth-child(4) { display: none; }
.heatmap { display: grid; grid-auto-columns: 11px; grid-auto-flow: column; grid-template-rows: repeat(7, 11px); gap: 3px; }
.heat-cell, .heat-legend i { background: var(--color-surface-raised); border-radius: 1px; height: 11px; width: 11px; }
.heat-cell.level--1 { background: transparent; }
.level-1 { background: color-mix(in srgb, var(--color-accent) 24%, var(--color-canvas)) !important; }.level-2 { background: color-mix(in srgb, var(--color-accent) 45%, var(--color-canvas)) !important; }.level-3 { background: color-mix(in srgb, var(--color-accent) 70%, var(--color-canvas)) !important; }.level-4 { background: var(--color-accent) !important; }
.heat-legend { align-items: center; color: var(--color-text-subtle); display: flex; font-size: 10px; gap: 4px; justify-content: flex-end; }
.rankings, .breakdowns { display: grid; gap: 10px; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); }
.table-wrap { overflow-x: auto; }
table { border-collapse: collapse; font-size: var(--font-size-ui-small); width: 100%; }
th { color: var(--color-text-subtle); font-weight: 400; text-align: left; }
th, td { border-bottom: 1px solid var(--color-border); padding: 5px 6px; white-space: nowrap; }
th:first-child, td:first-child { color: var(--color-text-subtle); width: 22px; }
th:last-child, td:last-child { text-align: right; }
td.name { max-width: 200px; overflow: hidden; text-overflow: ellipsis; }
td.name span, td.name small { display: block; overflow: hidden; text-overflow: ellipsis; }
td.name small { color: var(--color-text-subtle); }
tbody tr { cursor: default; }
.rankings section:first-child tbody tr, .rankings section:nth-child(2) tbody tr { cursor: pointer; }
tbody tr:hover { background: var(--color-surface-hover); }
.breakdown-row { align-items: center; border-bottom: 1px solid var(--color-border); display: grid; font-size: var(--font-size-ui-small); gap: 8px; grid-template-columns: minmax(90px, 1fr) auto auto; padding: 6px 0; }
.breakdown-row > span:nth-child(2) { color: var(--color-text-subtle); }
.note { color: var(--color-text-muted); line-height: 1.7; margin: 0; }
.error { color: var(--color-danger); margin: 0 14px 8px; }
.empty { color: var(--color-text-muted); padding: 30px; text-align: center; }
.spinning { display: inline-block; animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (max-width: 720px) { .filters { align-items: stretch; }.filters label, .filters select { max-width: none; }.rankings, .breakdowns { grid-template-columns: 1fr; }.summary-grid { grid-template-columns: repeat(2, 1fr); } }
</style>
