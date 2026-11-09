<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import { renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import { useInputSessionsStore } from "../../stores/inputSessions";
import ComposerBox from "./ComposerBox.vue";
import ToolActivity from "./ToolActivity.vue";
import DecisionCard from "./DecisionCard.vue";
import type { DecisionResponseIntent } from "./decisionInput";
import { chatIdForView, resolveChatView, setChatViewTask } from "./viewInstances";
import { formatChatTime as formatTime } from "./chatTime";
import { mergeMessageWindow, readingAnchor, withReadingAnchor, type ReadingAnchor } from "./historyWindow";
import type { Chat, ChatBlock, ChatBlockList, ChatHistory, ChatList, ChatMessage, Decision, QueuedMessage, Role, RunningTurn, Task, TaskList, TurnTarget, TurnTargetEntry, Workspace } from "./types";
import { errorText } from "./types";

const injected = inject<PluginCtx>("pluginCtx");
if (injected === undefined) throw new Error("TaskPane requires PluginPaneHost");
const ctx = injected;
const viewKey = ctx.instanceId;
const chatId = chatIdForView(viewKey);
const view = resolveChatView(viewKey);
const inputSessionId = `chat:${viewKey}`;
const inputs = useInputSessionsStore();
inputs.ensure({ id: inputSessionId, pluginId: "chat", paneType: "chat", instanceId: viewKey, params: { chatId }, label: "Chat input" });
const chat = ref<Chat | null>(null);
const roles = ref<Role[]>([]);
const tasks = ref<Task[]>([]);
const currentTaskId = ref(ctx.storage.get("currentTaskId", view?.initialTaskId ?? ""));
const currentTask = computed(() => tasks.value.find((task) => task.id === currentTaskId.value) ?? null);
const openTasks = computed(() => tasks.value.filter((task) => task.status === "open"));
const messages = ref<ChatMessage[]>([]);
const blocks = ref<ChatBlock[]>([]);
const decisions = ref<Decision[]>([]);
const queued = ref<QueuedMessage[]>([]);
const running = ref<Map<string, RunningTurn>>(new Map());
const turnTargets = ref<Map<string, TurnTargetEntry>>(new Map());
const error = ref("");
const loading = ref(false);
let historyGeneration = 0;
let bootstrapped = false;
let anchorTimer: ReturnType<typeof setTimeout> | undefined;
const hasOlder = ref(false);
const hasNewer = ref(false);
const renaming = ref(false);
const renameText = ref("");
const responseDrafts = ref<Record<string, string>>({});
const thread = ref<HTMLElement | null>(null);
const selectedRoleIds = computed({ get: () => inputs.session(inputSessionId)?.selectedRoleIds ?? [], set: (value: string[]) => inputs.patch(inputSessionId, { selectedRoleIds: value }) });
const composerExpanded = ref(false);
const composerRef = ref<{ focus: () => void } | null>(null);
const composerVisible = computed(() => Boolean(inputs.session(inputSessionId)?.pinned) || composerExpanded.value);
const memberRoles = computed(() => { const ids = new Set(chat.value?.member_role_ids ?? []); return roles.value.filter((role) => ids.has(role.id)); });
const currentRunning = computed(() => [...running.value.values()].filter((turn) => turn.task_id === currentTaskId.value));
const ended = computed(() => currentTask.value?.status === "ended");
function idempotencyKey(): string { return globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`; }
function mergeMessages(incoming: ChatMessage[]): void {
  const merged = mergeMessageWindow(messages.value, incoming);
  messages.value = merged.messages;
  if (merged.evictedOlder) hasOlder.value = true;
}
function mergeBlocks(incoming: ChatBlock[]): void {
  const byId = new Map(blocks.value.map((block) => [block.id, block]));
  for (const block of incoming) byId.set(block.id, block);
  blocks.value = [...byId.values()].filter((block) => block.task_id === currentTaskId.value && block.kind !== "other" && block.kind !== "decision").sort((a, b) => a.occurred_at - b.occurred_at || a.id.localeCompare(b.id)).slice(-1500);
}
function noteTarget(turnId: string, target: TurnTarget): void {
  const label = [target.agent, target.provider, target.model].filter(Boolean).join(" / ");
  turnTargets.value = new Map(turnTargets.value).set(turnId, { dispatchId: target.dispatch_id ?? "", roleId: target.role_id ?? "", roleName: target.role_name ?? "", label });
}
function noteTargets(values: Record<string, TurnTarget> | undefined): void { for (const [turnId, target] of Object.entries(values ?? {})) noteTarget(turnId, target); }
async function loadTasks(): Promise<void> {
  const result = await ctx.bus.request("chat:_:tasks:list", { chat_id: chatId, limit: 100 }) as TaskList;
  tasks.value = result.tasks;
  if (!tasks.value.some((task) => task.id === currentTaskId.value)) currentTaskId.value = openTasks.value[0]?.id ?? tasks.value[0]?.id ?? "";
}
function savedReadingAnchor(taskId: string): ReadingAnchor | undefined {
  return readingAnchor(ctx.storage.get("readingAnchors", {}), taskId);
}
function saveReadingAnchor(taskId = currentTaskId.value): void {
  const element = thread.value;
  if (!taskId || !element) return;
  const container = element.getBoundingClientRect();
  const candidates = [...element.querySelectorAll<HTMLElement>("[data-message-id]")];
  const closest = candidates.reduce<HTMLElement | undefined>((best, candidate) => {
    if (!best) return candidate;
    return Math.abs(candidate.getBoundingClientRect().top - container.top) < Math.abs(best.getBoundingClientRect().top - container.top) ? candidate : best;
  }, undefined);
  const messageId = closest?.dataset.messageId;
  const message = messages.value.find((item) => item.id === messageId);
  if (!message) return;
  ctx.storage.set("readingAnchors", withReadingAnchor(ctx.storage.get("readingAnchors", {}), taskId, { createdAt: message.created_at, messageId: message.id }));
}
function scheduleAnchorSave(): void {
  if (anchorTimer !== undefined) clearTimeout(anchorTimer);
  anchorTimer = setTimeout(() => { anchorTimer = undefined; saveReadingAnchor(); }, 150);
}
function groupAnchorId(group: TimelineGroup): string | undefined {
  return group.user?.id ?? group.items.find((item) => item.message)?.message?.id;
}
async function loadVisibleBlocks(taskId: string, generation: number, reset: boolean): Promise<void> {
  let after = 0;
  const first = messages.value[0];
  const last = messages.value.at(-1);
  for (let page = 0; page < 8; page += 1) {
    const request: Record<string, unknown> = { task_id: taskId, after };
    if (first && last) { if (page === 0) request.after = first.created_at; request.before = last.created_at + 1; }
    const blockResult = await ctx.bus.request("chat:_:blocks:list", request) as ChatBlockList;
    if (taskId !== currentTaskId.value || generation !== historyGeneration) return;
    if (page === 0 && reset) blocks.value = [];
    mergeBlocks(blockResult.blocks); noteTargets(blockResult.turn_targets);
    if (!blockResult.truncated || !blockResult.next_after) break;
    after = blockResult.next_after;
  }
}
async function loadHistory(around?: { createdAt: number; messageId: string }): Promise<void> {
  const taskId = currentTaskId.value;
  const generation = ++historyGeneration;
  if (!taskId) { messages.value = []; blocks.value = []; return; }
  loading.value = true; error.value = "";
  try {
    const request: Record<string, unknown> = { task_id: taskId, include_messages: true, limit: 50 };
    if (around) { request.around = around.createdAt; request.message_id = around.messageId; }
    const result = await ctx.bus.request("chat:_:history", request) as ChatHistory;
    if (taskId !== currentTaskId.value || generation !== historyGeneration) return;
    messages.value = []; mergeMessages(result.messages ?? []);
    hasOlder.value = result.has_more === true; hasNewer.value = result.has_more_after === true;
    noteTargets(result.turn_targets);
    running.value = new Map((result.running_turns ?? []).map((turn) => [turn.turn_id, turn]));
    queued.value = (result.queued_messages ?? []).filter((entry) => entry.task_id === taskId);
    const decisionResult = await ctx.bus.request("chat:_:decisions:list", { task_id: taskId }) as { decisions: Decision[] };
    if (taskId !== currentTaskId.value || generation !== historyGeneration) return;
    decisions.value = decisionResult.decisions;
    await loadVisibleBlocks(taskId, generation, true);
    await nextTick();
    if (around) document.querySelector(`[data-message-id="${around.messageId}"]`)?.scrollIntoView({ block: "center" });
    if (thread.value) await renderMermaidIn(thread.value);
  } catch (cause) { if (generation === historyGeneration) error.value = errorText(cause); } finally { if (generation === historyGeneration) loading.value = false; }
}
async function loadOlder(): Promise<void> {
  const first = messages.value[0]; if (!first || !hasOlder.value || loading.value) return;
  const element = thread.value; const previousHeight = element?.scrollHeight ?? 0;
  const taskId = currentTaskId.value;
  const generation = historyGeneration;
  const result = await ctx.bus.request("chat:_:history", { task_id: taskId, include_messages: true, limit: 50, before: first.created_at, before_id: first.id }) as ChatHistory;
  if (taskId !== currentTaskId.value || generation !== historyGeneration) return;
  mergeMessages(result.messages ?? []); hasOlder.value = result.has_more === true;
  await loadVisibleBlocks(taskId, generation, false);
  if (taskId !== currentTaskId.value || generation !== historyGeneration) return;
  await nextTick(); if (element) element.scrollTop += element.scrollHeight - previousHeight;
}
async function loadNewer(): Promise<void> {
  const last = messages.value.at(-1); if (!last || !hasNewer.value || loading.value) return;
  const taskId = currentTaskId.value;
  const generation = historyGeneration;
  const result = await ctx.bus.request("chat:_:history", { task_id: taskId, include_messages: true, limit: 50, after: last.created_at, after_id: last.id }) as ChatHistory;
  if (taskId !== currentTaskId.value || generation !== historyGeneration) return;
  mergeMessages(result.messages ?? []); hasNewer.value = result.has_more_after === true;
  await loadVisibleBlocks(taskId, generation, false);
}
function onScroll(): void {
  const element = thread.value; if (!element) return;
  scheduleAnchorSave();
  if (element.scrollTop < 80) void loadOlder().catch((cause) => { error.value = errorText(cause); });
  if (element.scrollHeight - element.scrollTop - element.clientHeight < 80) void loadNewer().catch((cause) => { error.value = errorText(cause); });
}
async function bootstrap(): Promise<void> {
  try {
    const [chatResult, workspace] = await Promise.all([ctx.bus.request("chat:_:chats:list", {}) as Promise<ChatList>, ctx.bus.request("chat:_:workspace:get", {}) as Promise<Workspace>]);
    chat.value = chatResult.chats.find((item) => item.id === chatId) ?? null; roles.value = workspace.roles;
    await loadTasks(); bootstrapped = true; await loadHistory(savedReadingAnchor(currentTaskId.value));
  } catch (cause) { error.value = errorText(cause); }
}
watch(currentTaskId, (taskId, previous) => {
  if (taskId === previous) return;
  if (previous) saveReadingAnchor(previous);
  if (!taskId) { historyGeneration += 1; messages.value = []; blocks.value = []; return; }
  ctx.storage.set("currentTaskId", taskId); inputs.setTask(inputSessionId, taskId);
  setChatViewTask(viewKey, taskId);
  window.dispatchEvent(new CustomEvent("viewer:task-current", { detail: { viewKey, taskId } }));
  if (bootstrapped) void loadHistory(savedReadingAnchor(taskId));
}, { flush: "sync" });
watch(currentTask, (task) => {  ctx.setChrome({ title: `${chat.value?.name ?? "Chat"} · ${task?.title ?? "Tasks"}` }); });
async function createTask(): Promise<void> {
  try {
    const result = await ctx.bus.request("chat:_:tasks:create", { chat_id: chatId, idempotency_key: idempotencyKey() }) as { task: Task };
    tasks.value = [...tasks.value.filter((task) => task.id !== result.task.id), result.task]; currentTaskId.value = result.task.id;
  } catch (cause) { error.value = errorText(cause); }
}
function beginRename(): void { renameText.value = currentTask.value?.title ?? ""; renaming.value = true; }
async function saveRename(): Promise<void> {
  const task = currentTask.value; if (!task || !renameText.value.trim()) return;
  try {
    const result = await ctx.bus.request("chat:_:tasks:patch", { task_id: task.id, title: renameText.value.trim(), title_revision: task.title_revision }) as { task: Task };
    tasks.value = tasks.value.map((item) => item.id === task.id ? result.task : item); renaming.value = false;
  } catch (cause) { error.value = errorText(cause); }
}
async function togglePin(): Promise<void> {
  const task = currentTask.value; if (!task) return;
  try { const result = await ctx.bus.request("chat:_:tasks:pin", { task_id: task.id, pinned: !task.pinned }) as { task: Task }; tasks.value = tasks.value.map((item) => item.id === task.id ? result.task : item); } catch (cause) { error.value = errorText(cause); }
}
async function endTask(): Promise<void> {
  const task = currentTask.value; if (!task || !window.confirm(`结束 Task「${task.title}」？结束后只读，并生成最终摘要。`)) return;
  try { const result = await ctx.bus.request("chat:_:tasks:end", { task_id: task.id }) as { task: Task }; tasks.value = tasks.value.map((item) => item.id === task.id ? result.task : item); } catch (cause) { error.value = errorText(cause); }
}
async function retrySummary(): Promise<void> { if (currentTaskId.value) await ctx.bus.request("chat:_:tasks:summary-retry", { task_id: currentTaskId.value }).catch((cause) => { error.value = errorText(cause); }); }
async function exportTask(format: "markdown" | "json"): Promise<void> {
  if (!currentTask.value) return;
  try {
    const result = await ctx.bus.request("chat:_:tasks:export", { task_id: currentTaskId.value, format }) as { content?: string };
    const content = result.content ?? JSON.stringify(result, null, 2);
    const blob = new Blob([content], { type: format === "json" ? "application/json" : "text/markdown" }); const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a"); anchor.href = url; anchor.download = `${currentTask.value.title}.${format === "json" ? "json" : "md"}`; anchor.click(); URL.revokeObjectURL(url);
  } catch (cause) { error.value = errorText(cause); }
}
function openTasksPage(): void { void ctx.shell.open({ paneType: "chat-tasks", instanceId: "main" }); }
async function openDecisionSource(decision: Decision): Promise<void> {
  const target = await ctx.bus.request("chat:_:decisions:open", { decision_id: decision.id }) as { created_at: number; source_message_id: string };
  if (currentTaskId.value !== decision.task_id) currentTaskId.value = decision.task_id;
  await loadHistory({ createdAt: target.created_at, messageId: target.source_message_id });
}
async function respondDecision(intent: DecisionResponseIntent): Promise<void> {
  try { await ctx.bus.request("chat:_:decisions:respond", { decision_id: intent.decisionId, response: intent.response, expected_version: intent.expectedVersion }, { timeout: 90_000 }); responseDrafts.value[intent.decisionId] = ""; } catch (cause) { error.value = errorText(cause); }
}
async function cancelDecision(decision: Decision): Promise<void> { await ctx.bus.request("chat:_:decisions:cancel", { decision_id: decision.id, expected_version: decision.version }).catch((cause) => { error.value = errorText(cause); }); }
async function cancelQueued(entry: QueuedMessage): Promise<void> { await ctx.bus.request("chat:_:queued-cancel", { chat_id: chatId, dispatch_id: entry.dispatch_id }).catch((cause) => { error.value = errorText(cause); }); }
function handleComposerFocusOut(event: FocusEvent): void {
  if (inputs.session(inputSessionId)?.pinned) return;
  const shell = event.currentTarget;
  const next = event.relatedTarget;
  if (shell instanceof HTMLElement && next instanceof Node && shell.contains(next)) return;
  window.setTimeout(() => {
    if (!inputs.session(inputSessionId)?.pinned) composerExpanded.value = false;
  }, 0);
}
function openComposer(): void {
  inputs.activate(inputSessionId);
  composerExpanded.value = true;
  void nextTick(() => composerRef.value?.focus());
}
function editQueued(entry: QueuedMessage): void {
  inputs.beginQueuedEdit(inputSessionId, entry.task_id, entry.dispatch_id, entry.text);
  openComposer();
}
async function stopTurn(turnId?: string): Promise<void> { await ctx.bus.request("chat:_:stop", { chat_id: chatId, turn_id: turnId ?? "" }).catch((cause) => { error.value = errorText(cause); }); }
function continueTurn(turnId: string): void {
  inputs.patch(inputSessionId, { continueTurnId: turnId });
  openComposer();
}
function citeMessage(messageId: string): void { const session = inputs.session(inputSessionId); if (session) inputs.setText(inputSessionId, `${session.text}${session.text ? " " : ""}@${messageId} `); }
interface TimelineItem { id: string; at: number; message?: ChatMessage; block?: ChatBlock }
interface TimelineGroup { id: string; turnId: string; at: number; user?: ChatMessage; roleName: string; roleId: string; items: TimelineItem[]; decisions: Decision[] }
const timeline = computed<TimelineGroup[]>(() => {
  const groups = new Map<string, TimelineGroup>();
  const textTurns = new Set(blocks.value.filter((block) => block.kind === "agent_text").map((block) => block.turn_id));
  for (const message of messages.value) {
    if (message.role === "assistant" && textTurns.has(message.turn_id)) continue;
    const id = message.role === "user" ? `user:${message.id}` : `turn:${message.turn_id}`;
    const group = groups.get(id) ?? { id, turnId: message.turn_id, at: message.created_at, user: message.role === "user" ? message : undefined, roleName: message.sender.role_name ?? "Agent", roleId: message.sender.role_id ?? "", items: [], decisions: [] };
    group.items.push({ id: message.id, at: message.created_at, message }); group.at = Math.min(group.at, message.created_at); groups.set(id, group);
  }
  for (const block of blocks.value) {
    const id = `turn:${block.turn_id}`;
    const group = groups.get(id) ?? { id, turnId: block.turn_id, at: block.occurred_at, roleName: block.role_name ?? "Agent", roleId: block.role_id ?? "", items: [], decisions: [] };
    group.items.push({ id: block.id, at: block.occurred_at, block }); group.at = Math.min(group.at, block.occurred_at); groups.set(id, group);
  }
  for (const decision of decisions.value) groups.get(`turn:${decision.source_turn_id}`)?.decisions.push(decision);
  for (const group of groups.values()) group.items.sort((a, b) => a.at - b.at || a.id.localeCompare(b.id));
  return [...groups.values()].sort((a, b) => a.at - b.at || a.id.localeCompare(b.id));
});
function queuedFor(turnId: string): QueuedMessage[] { return queued.value.filter((entry) => entry.dispatch_id === turnId); }
function visibleMessage(message: ChatMessage): string {
  const decision = decisions.value.find((item) => item.source_message_id === message.id);
  const bytes = new TextEncoder().encode(message.text);
  const decoder = new TextDecoder();
  return decision && decision.status !== 'invalid' ? `${decoder.decode(bytes.slice(0, decision.marker_start))}${decoder.decode(bytes.slice(decision.marker_end))}` : message.text;
}
function markdown(text: string): string { return renderMarkdown(text); }
function roleTarget(turnId: string): string { return turnTargets.value.get(turnId)?.label ?? ""; }
function onMessage(frame: { value?: unknown }): void { const value = frame.value as ChatMessage | undefined; if (value?.chat_id === chatId && value.task_id === currentTaskId.value) mergeMessages([value]); }
function onBlock(frame: { value?: unknown }): void { const value = frame.value as ChatBlock | undefined; if (value?.chat_id === chatId && value.task_id === currentTaskId.value) mergeBlocks([value]); }
function onTurn(frame: { value?: unknown }): void {
  const value = frame.value as { chat_id?: string; task_id?: string; turn_id?: string; role_id?: string; role_name?: string; phase?: string; agent?: string; provider?: string; model?: string; dispatch_id?: string } | undefined;
  if (!value?.chat_id || value.chat_id !== chatId || !value.turn_id) return;
  const next = new Map(running.value);
  if (value.phase === "started") next.set(value.turn_id, { turn_id: value.turn_id, task_id: value.task_id ?? "", role_id: value.role_id ?? "", role_name: value.role_name });
  if (value.phase === "completed") next.delete(value.turn_id); running.value = next;
  if (value.agent || value.provider || value.model) noteTarget(value.turn_id, value as TurnTarget);
  if (value.task_id) tasks.value = tasks.value.map((task) => task.id === value.task_id ? { ...task, running: value.phase !== "completed" } : task);
}
function onQueue(): void { if (currentTaskId.value) void loadHistory(savedReadingAnchor(currentTaskId.value)); }
function onTask(frame: { value?: unknown }): void {
  const task = frame.value as Task | undefined; if (!task?.id) return;
  const byId = new Map(tasks.value.map((item) => [item.id, item])); byId.set(task.id, task);
  tasks.value = [...byId.values()].sort((a, b) => a.created_at - b.created_at || a.id.localeCompare(b.id));
}
function onDecision(frame: { value?: unknown }): void {
  const decision = frame.value as Decision | undefined; if (!decision?.id || decision.task_id !== currentTaskId.value) return;
  const byId = new Map(decisions.value.map((item) => [item.id, item])); byId.set(decision.id, decision); decisions.value = [...byId.values()];
}
function onSummary(frame: { value?: unknown }): void {
  const value = frame.value as { task_id?: string; text?: string; status?: Task["summary_status"]; final?: boolean; summary_revision?: number } | undefined; if (!value?.task_id) return;
  tasks.value = tasks.value.map((task) => task.id === value.task_id ? { ...task, summary_preview: value.text ?? task.summary_preview, summary_status: value.status, summary_final: value.final, summary_revision: value.summary_revision } : task);
}
function onExternalSourceOpen(event: Event): void {
  const detail = (event as CustomEvent<{ viewKey?: string; anchor?: ReadingAnchor }>).detail;
  if (detail?.viewKey === viewKey && detail.anchor) void loadHistory(detail.anchor);
}
onMounted(() => {
  inputs.activate(inputSessionId); if (currentTaskId.value) { inputs.setTask(inputSessionId, currentTaskId.value); setChatViewTask(viewKey, currentTaskId.value); window.dispatchEvent(new CustomEvent("viewer:task-current", { detail: { viewKey, taskId: currentTaskId.value } })); }
  window.addEventListener("viewer:task-open-source", onExternalSourceOpen);
  ctx.bus.subscribe(`chat:${chatId}:message`, onMessage); ctx.bus.subscribe(`chat:${chatId}:block`, onBlock); ctx.bus.subscribe("chat:_:turn", onTurn); ctx.bus.subscribe("chat:_:queue", onQueue); ctx.bus.subscribe("chat:_:task", onTask); ctx.bus.subscribe("chat:_:decision", onDecision); ctx.bus.subscribe("chat:_:task-summary", onSummary); void bootstrap();
});
onBeforeUnmount(() => {
  if (anchorTimer !== undefined) clearTimeout(anchorTimer);
  saveReadingAnchor();
  window.dispatchEvent(new CustomEvent("viewer:task-current", { detail: { viewKey, taskId: "" } }));
  window.removeEventListener("viewer:task-open-source", onExternalSourceOpen);
  ctx.bus.unsubscribe(`chat:${chatId}:message`, onMessage); ctx.bus.unsubscribe(`chat:${chatId}:block`, onBlock); ctx.bus.unsubscribe("chat:_:turn", onTurn); ctx.bus.unsubscribe("chat:_:queue", onQueue); ctx.bus.unsubscribe("chat:_:task", onTask); ctx.bus.unsubscribe("chat:_:decision", onDecision); ctx.bus.unsubscribe("chat:_:task-summary", onSummary);
});
</script>
<template>
  <div class="task-chat">
    <header class="task-header">
      <select v-model="currentTaskId" class="task-picker" aria-label="Current Task"><option v-for="task in openTasks" :key="task.id" :value="task.id">{{ task.title }}</option><option v-if="currentTask?.status === 'ended'" :value="currentTask.id">{{ currentTask.title }}（已结束）</option></select>
      <span v-if="currentRunning.length" class="running-label"><i class="bi bi-arrow-repeat spin" /> {{ currentRunning.length }}</span>
      <button class="icon-button" type="button" title="新建 Task" @click="createTask"><i class="bi bi-plus-lg" /></button><button class="icon-button" type="button" title="重命名" @click="beginRename"><i class="bi bi-pencil" /></button><button class="icon-button" type="button" :title="currentTask?.pinned ? '取消固定' : '固定到侧边栏'" :disabled="ended" @click="togglePin"><i class="bi" :class="currentTask?.pinned ? 'bi-pin-angle-fill' : 'bi-pin-angle'" /></button><button class="icon-button" type="button" title="所有 Tasks" @click="openTasksPage"><i class="bi bi-list-task" /></button><button class="icon-button" type="button" title="导出 Markdown" @click="exportTask('markdown')"><i class="bi bi-filetype-md" /></button><button class="icon-button" type="button" title="导出 JSON" @click="exportTask('json')"><i class="bi bi-filetype-json" /></button><button v-if="!ended" class="icon-button danger" type="button" title="结束 Task（只读）" @click="endTask"><i class="bi bi-check2-circle" /></button>
    </header>
    <div v-if="renaming" class="rename-row"><input v-model="renameText" @keyup.enter="saveRename"><button type="button" @click="saveRename">保存</button><button type="button" @click="renaming = false">取消</button></div><div v-if="error" class="error-row">{{ error }}<button type="button" @click="error = ''">×</button></div>
    <section class="task-body">
      <div v-if="currentTask?.summary_preview" class="task-summary"><details><summary>Task 摘要 · {{ currentTask.summary_status }}<span v-if="currentTask.summary_final"> · final</span></summary><p>{{ currentTask.summary_preview }}</p></details><button v-if="currentTask.summary_status === 'failed' || currentTask.summary_status === 'missing'" type="button" @click="retrySummary">重试摘要</button></div>
      <div ref="thread" class="timeline" @scroll.passive="onScroll">
        <button v-if="hasOlder" class="page-button" type="button" @click="loadOlder">加载更早消息</button><div v-if="loading && messages.length === 0" class="empty-state">加载中…</div>
        <article v-for="group in timeline" :key="group.id" class="turn-box" :class="{ user: group.user }" :data-message-id="groupAnchorId(group)">
          <header class="turn-header"><strong>{{ group.user ? 'User' : group.roleName }}</strong><span v-if="!group.user && roleTarget(group.turnId)">{{ roleTarget(group.turnId) }}</span><time>{{ formatTime(group.at) }}</time><button v-if="group.user" class="icon-button" type="button" title="引用消息" @click="citeMessage(group.user.id)"><i class="bi bi-quote" /></button><button v-if="!group.user && !ended" class="continue-button" type="button" title="继续此 Turn 的 session" @click="continueTurn(group.turnId)">继续 session</button><button v-if="!group.user && running.has(group.turnId)" class="icon-button" type="button" title="停止 Turn" @click="stopTurn(group.turnId)"><i class="bi bi-stop-circle" /></button></header>
          <template v-for="item in group.items" :key="item.id"><div v-if="item.message" class="markdown-body" v-html="markdown(visibleMessage(item.message))" /><ToolActivity v-else-if="item.block" :block="item.block" :time="formatTime(item.block.occurred_at)" /></template>
          <div v-for="entry in queuedFor(group.turnId)" :key="entry.dispatch_id" class="queued-row">排队 #{{ entry.position }}<button type="button" @click="editQueued(entry)">编辑</button><button type="button" @click="cancelQueued(entry)">取消</button></div>
          <DecisionCard v-for="decision in group.decisions" :key="decision.id" :decision="decision" :draft="responseDrafts[decision.id] ?? ''" @update:draft="responseDrafts[decision.id] = $event" @respond="respondDecision" @cancel="cancelDecision(decision)" @open="openDecisionSource(decision)" />
        </article>
        <button v-if="hasNewer" class="page-button" type="button" @click="loadNewer">加载更新消息</button><div v-if="!loading && timeline.length === 0" class="empty-state">这个 Task 还没有消息。</div>
      </div>
    </section>
    <div v-if="ended" class="ended-banner">Task 已结束。输入并发送消息将继续此 Task。</div>
    <footer v-if="currentTaskId && composerVisible" class="task-composer" aria-label="Task composer" @focusout="handleComposerFocusOut">
      <ComposerBox ref="composerRef" v-model:selected-role-ids="selectedRoleIds" :roles="memberRoles" :chat-id="chatId" :context-id="inputSessionId" :draft-task-id="currentTaskId" />
    </footer>
    <button
      v-else-if="currentTaskId"
      type="button"
      class="composer-overlay-button"
      :class="{ active: inputs.activeId === inputSessionId }"
      :title="inputs.session(inputSessionId)?.text ? '打开已保存的输入' : '打开输入框'"
      :aria-label="inputs.session(inputSessionId)?.text ? '打开已保存的输入' : '打开输入框'"
      @click="openComposer"
    >
      <i class="bi" :class="inputs.session(inputSessionId)?.text ? 'bi-pencil-square' : 'bi-chat-square-text'" />
      <span v-if="inputs.session(inputSessionId)?.text" class="composer-overlay-dot" />
    </button>
  </div>
</template>
<style scoped>
.task-chat{display:flex;flex-direction:column;height:100%;min-height:0;overflow:hidden;position:relative;color:var(--color-text)}.task-header{align-items:center;border-bottom:1px solid var(--color-border);display:flex;flex:0 0 auto;gap:4px;padding:5px 7px}.task-picker{background:transparent;border:0;color:var(--color-text);flex:1;font-weight:600;min-width:0}.icon-button{background:transparent;border:0;border-radius:var(--radius-sm);color:var(--color-text-muted);cursor:pointer;padding:4px 6px}.icon-button:hover{background:var(--color-surface-hover);color:var(--color-text)}.icon-button:disabled{opacity:.35}.icon-button.danger{color:var(--bs-danger)}.running-label{color:var(--color-text-muted);font-size:var(--font-size-ui-small)}.spin{display:inline-block;animation:spin 1s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}.rename-row,.error-row{align-items:center;display:flex;gap:5px;padding:5px 8px}.rename-row input,.task-filters input,.decision-free input{background:var(--color-surface);border:1px solid var(--color-border);color:var(--color-text);flex:1;min-width:0;padding:4px 6px}.error-row{color:var(--bs-danger);font-size:var(--font-size-ui-small)}.error-row button{margin-left:auto}.task-body{display:flex;flex:1;flex-direction:column;min-height:0;overflow:hidden}.timeline{flex:1;min-height:0;overflow-y:auto;padding:8px}.task-composer{border-top:1px solid var(--color-border);flex:0 0 auto;max-height:65vh;overflow-y:auto;position:relative;z-index:2}
.composer-overlay-button {
  align-items: center;
  backdrop-filter: blur(6px);
  background: color-mix(in srgb, var(--color-surface-raised) 72%, transparent);
  border: 1px solid color-mix(in srgb, var(--color-border) 72%, transparent);
  border-radius: 999px;
  bottom: 8px;
  color: var(--color-text-muted);
  display: inline-flex;
  height: 38px;
  justify-content: center;
  opacity: .72;
  position: absolute;
  right: 8px;
  width: 38px;
  z-index: 8;
}
.composer-overlay-button:hover,
.composer-overlay-button.active { color: var(--color-text); opacity: 1; }
.composer-overlay-dot { background: var(--color-accent); border-radius: 50%; height: 6px; position: absolute; right: 4px; top: 4px; width: 6px; }.turn-box{border-bottom:1px solid color-mix(in srgb,var(--color-border) 55%,transparent);margin:0 auto;max-width:980px;padding:7px 3px 10px}.turn-header{align-items:center;color:var(--color-text-muted);display:flex;font-size:var(--font-size-ui-small);gap:8px;margin-bottom:4px}.turn-header strong{color:var(--color-text)}.turn-header time{margin-left:auto}.continue-button,.source-link{background:transparent;border:0;color:var(--color-text-muted);font-size:var(--font-size-ui-small);padding:2px}.markdown-body{font-size:var(--markdown-body-font-size,15px);line-height:1.55;overflow-wrap:anywhere;user-select:text}.markdown-body :deep(pre){overflow:auto}.markdown-body :deep(p:last-child){margin-bottom:0}.queued-row{color:var(--color-text-muted);display:flex;font-size:var(--font-size-ui-small);gap:8px;padding-top:4px}.queued-row button,.task-summary button,.all-tasks button,.decision-card button{font-size:var(--font-size-ui-small)}.decision-card{border-left:2px solid var(--color-accent);display:flex;flex-wrap:wrap;gap:6px;margin-top:8px;padding:5px 8px}.decision-card.error,.parse-warning{color:var(--bs-danger)}.decision-status{color:var(--color-text-muted);font-size:var(--font-size-ui-small)}.decision-free{display:flex;flex:1 1 260px;gap:4px}.source-link{flex-basis:100%;text-align:left}.page-button{color:var(--color-text-muted);display:block;margin:4px auto 10px}.empty-state,.ended-banner{color:var(--color-text-muted);padding:20px;text-align:center}.ended-banner{border-top:1px solid var(--color-border);padding:8px}.task-summary{border-bottom:1px solid var(--color-border);color:var(--color-text-muted);display:flex;font-size:var(--font-size-ui-small);padding:5px 9px}.task-summary details{flex:1}.task-summary p{white-space:pre-wrap}.all-tasks{flex:1;overflow:auto;padding:8px}.task-filters{display:flex;gap:5px;margin-bottom:8px}.all-task-columns,.all-task-row{align-items:center;display:grid;gap:10px;grid-template-columns:minmax(160px,1.2fr) 80px minmax(180px,2fr) 70px 90px;padding:6px;text-align:left}.all-task-columns{color:var(--color-text-muted);font-size:var(--font-size-ui-small)}.all-task-row{background:transparent;border:0;border-top:1px solid var(--color-border);color:var(--color-text);width:100%}.all-task-row:hover{background:var(--color-surface-hover)}.all-task-row small{color:var(--color-text-muted);display:block}.summary-preview{color:var(--color-text-muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}@media(max-width:700px){.all-task-columns{display:none}.all-task-row{grid-template-columns:1fr 70px}.all-task-row>:nth-child(n+3){display:none}.turn-header>span{display:none}}
</style>
