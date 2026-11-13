<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, watch, watchEffect } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { PaneChromeAction } from "../../stores/paneChrome";
import { renderMermaidIn } from "../../utils/markdownRender";
import { useInputSessionsStore } from "../../stores/inputSessions";
import ComposerBox from "./ComposerBox.vue";
import TurnContent from "./TurnContent.vue";
import { activityDisplayable } from "./toolPresentation";
import type { ChatTimelineItem } from "./turnContent";
import DecisionCard from "./DecisionCard.vue";
import { decisionResponsePayload, type DecisionResponseIntent } from "./decisionInput";
import { chatIdForView, resolveChatView, setChatViewTask } from "./viewInstances";
import { formatChatTime as formatTime } from "./chatTime";
import { mergeMessageWindow, readingAnchor, withReadingAnchor, type MessageWindowEviction, type ReadingAnchor } from "./historyWindow";
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
const summaryOpen = ref(false);
const responseDrafts = ref<Record<string, string>>({});
const thread = ref<HTMLElement | null>(null);
const selectedRoleIds = computed({ get: () => inputs.session(inputSessionId)?.selectedRoleIds ?? [], set: (value: string[]) => inputs.patch(inputSessionId, { selectedRoleIds: value }) });
const composerExpanded = ref(false);
const composerRef = ref<{ focus: () => void } | null>(null);
const composerVisible = computed(() => Boolean(inputs.session(inputSessionId)?.pinned) || composerExpanded.value);
const memberRoles = computed(() => { const ids = new Set(chat.value?.member_role_ids ?? []); return roles.value.filter((role) => ids.has(role.id)); });
const currentRunning = computed(() => [...running.value.values()].filter((turn) => turn.task_id === currentTaskId.value));
const ended = computed(() => currentTask.value?.status === "ended");
function mergeMessages(incoming: ChatMessage[], evict: MessageWindowEviction = "older"): void {
  const merged = mergeMessageWindow(messages.value, incoming, undefined, evict);
  messages.value = merged.messages;
  if (merged.evictedOlder) hasOlder.value = true;
  if (merged.evictedNewer) hasNewer.value = true;
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
function messageOffset(messageId: string): number | undefined {
  return thread.value?.querySelector<HTMLElement>(`[data-message-id="${messageId}"]`)?.getBoundingClientRect().top;
}
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
    if (!around && thread.value) thread.value.scrollTop = thread.value.scrollHeight;
  } catch (cause) { if (generation === historyGeneration) error.value = errorText(cause); } finally { if (generation === historyGeneration) loading.value = false; }
}
async function loadOlder(): Promise<void> {
  const first = messages.value[0]; if (!first || !hasOlder.value || loading.value) return;
  const element = thread.value; const previousHeight = element?.scrollHeight ?? 0;
  const previousAnchorTop = messageOffset(first.id);
  const taskId = currentTaskId.value;
  const generation = historyGeneration;
  const result = await ctx.bus.request("chat:_:history", { task_id: taskId, include_messages: true, limit: 50, before: first.created_at, before_id: first.id }) as ChatHistory;
  if (taskId !== currentTaskId.value || generation !== historyGeneration) return;
  mergeMessages(result.messages ?? [], "newer"); hasOlder.value = result.has_more === true;
  await loadVisibleBlocks(taskId, generation, false);
  if (taskId !== currentTaskId.value || generation !== historyGeneration) return;
  await nextTick();
  if (element) {
    const nextAnchorTop = messageOffset(first.id);
    element.scrollTop += previousAnchorTop !== undefined && nextAnchorTop !== undefined
      ? nextAnchorTop - previousAnchorTop
      : element.scrollHeight - previousHeight;
  }
}
async function loadNewer(): Promise<void> {
  const last = messages.value.at(-1); if (!last || !hasNewer.value || loading.value) return;
  const element = thread.value; const previousAnchorTop = messageOffset(last.id);
  const taskId = currentTaskId.value;
  const generation = historyGeneration;
  const result = await ctx.bus.request("chat:_:history", { task_id: taskId, include_messages: true, limit: 50, after: last.created_at, after_id: last.id }) as ChatHistory;
  if (taskId !== currentTaskId.value || generation !== historyGeneration) return;
  mergeMessages(result.messages ?? []); hasNewer.value = result.has_more_after === true;
  await loadVisibleBlocks(taskId, generation, false);
  if (taskId !== currentTaskId.value || generation !== historyGeneration) return;
  await nextTick();
  const nextAnchorTop = messageOffset(last.id);
  if (element && previousAnchorTop !== undefined && nextAnchorTop !== undefined) element.scrollTop += nextAnchorTop - previousAnchorTop;
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
  summaryOpen.value = false;
  if (previous) saveReadingAnchor(previous);
  if (!taskId) { historyGeneration += 1; messages.value = []; blocks.value = []; return; }
  ctx.storage.set("currentTaskId", taskId); inputs.setTask(inputSessionId, taskId);
  setChatViewTask(viewKey, taskId);
  window.dispatchEvent(new CustomEvent("viewer:task-current", { detail: { viewKey, taskId } }));
  if (bootstrapped) void loadHistory(savedReadingAnchor(taskId));
}, { flush: "sync" });
watchEffect(() => {
  const task = currentTask.value;
  const actions: PaneChromeAction[] = [];
  if (currentRunning.value.length > 0) {
    actions.push({
      id: "stop-task",
      title: `停止当前 Task（${currentRunning.value.length} 个 Turn）`,
      icon: "bi-stop-circle",
      variant: "danger",
      run: stopCurrentTask,
    });
  }
  if (task) {
    actions.push({ id: "rename-task", title: "重命名 Task", icon: "bi-pencil", run: beginRename });
    if (task.status !== "ended") {
      actions.push({
        id: "pin-task",
        title: task.pinned ? "取消固定到侧边栏" : "固定到侧边栏",
        icon: task.pinned ? "bi-pin-angle-fill" : "bi-pin-angle",
        active: task.pinned,
        run: togglePin,
      });
    }
  }
  actions.push({ id: "all-tasks", title: "所有 Tasks", icon: "bi-list-task", run: openTasksPage });
  if (task) {
    if (task.status !== "ended") {
      actions.push({ id: "end-task", title: "结束 Task", icon: "bi-check2-circle", variant: "danger", run: endTask });
    }
  }
  ctx.setChrome({
    title: `${chat.value?.name ?? "Chat"} · ${task?.title ?? "Tasks"}`,
    status: currentRunning.value.length > 0 ? `${currentRunning.value.length} running` : "",
    statusClass: "text-primary",
    actions,
  });
});
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
function openTasksPage(): void { void ctx.shell.open({ paneType: "chat-tasks", instanceId: "main" }); }
async function openDecisionSource(decision: Decision): Promise<void> {
  const target = await ctx.bus.request("chat:_:decisions:open", { decision_id: decision.id }) as { created_at: number; source_message_id: string };
  if (currentTaskId.value !== decision.task_id) currentTaskId.value = decision.task_id;
  await loadHistory({ createdAt: target.created_at, messageId: target.source_message_id });
}
async function respondDecision(intent: DecisionResponseIntent): Promise<void> {
  try { await ctx.bus.request("chat:_:decisions:respond", decisionResponsePayload(intent), { timeout: 90_000 }); responseDrafts.value[intent.decisionId] = ""; } catch (cause) { error.value = errorText(cause); }
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
async function stopTurn(turnId: string): Promise<void> { await ctx.bus.request("chat:_:stop", { chat_id: chatId, turn_id: turnId }).catch((cause) => { error.value = errorText(cause); }); }
async function stopCurrentTask(): Promise<void> {
  await Promise.all(currentRunning.value.map((turn) => stopTurn(turn.turn_id)));
}
function citeMessage(messageId: string): void { const session = inputs.session(inputSessionId); if (session) inputs.setText(inputSessionId, `${session.text}${session.text ? " " : ""}@${messageId} `); }
interface TimelineGroup { id: string; turnId: string; at: number; user?: ChatMessage; roleName: string; roleId: string; items: ChatTimelineItem[]; decisions: Decision[] }
const timeline = computed<TimelineGroup[]>(() => {
  const groups = new Map<string, TimelineGroup>();
  for (const message of messages.value) {
    const id = message.role === "user" ? `user:${message.id}` : `turn:${message.turn_id}`;
    const group = groups.get(id) ?? { id, turnId: message.turn_id, at: message.created_at, user: message.role === "user" ? message : undefined, roleName: message.sender.role_name ?? "Agent", roleId: message.sender.role_id ?? "", items: [], decisions: [] };
    group.items.push({ id: message.id, at: message.created_at, kind: "message", message }); group.at = Math.min(group.at, message.created_at); groups.set(id, group);
  }
  for (const block of blocks.value) {
    // Agent text is rendered from ChatMessage above, never as a small tool row.
    if (!activityDisplayable(block)) continue;
    const id = `turn:${block.turn_id}`;
    const group = groups.get(id) ?? { id, turnId: block.turn_id, at: block.occurred_at, roleName: block.role_name ?? "Agent", roleId: block.role_id ?? "", items: [], decisions: [] };
    group.items.push({ id: block.id, at: block.occurred_at, kind: "activity", block }); group.at = Math.min(group.at, block.occurred_at); groups.set(id, group);
  }
  for (const decision of decisions.value) groups.get(`turn:${decision.source_turn_id}`)?.decisions.push(decision);
  for (const group of groups.values()) group.items.sort((a, b) => a.at - b.at || a.id.localeCompare(b.id));
  return [...groups.values()].sort((a, b) => a.at - b.at || a.id.localeCompare(b.id));
});
function queuedFor(turnId: string): QueuedMessage[] { return queued.value.filter((entry) => entry.dispatch_id === turnId); }
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
    <div v-if="renaming" class="rename-row"><input v-model="renameText" @keyup.enter="saveRename"><button type="button" @click="saveRename">保存</button><button type="button" @click="renaming = false">取消</button></div><div v-if="error" class="error-row">{{ error }}<button type="button" @click="error = ''">×</button></div>
    <section class="task-body">
      <button
        v-if="currentTask && (currentTask.summary_preview || currentTask.summary_status)"
        class="summary-trigger"
        type="button"
        :class="{ active: summaryOpen }"
        title="查看 Task 摘要"
        aria-label="查看 Task 摘要"
        :aria-expanded="summaryOpen"
        @click="summaryOpen = !summaryOpen"
      ><i class="bi bi-card-text" /></button>
      <button v-if="summaryOpen" class="summary-backdrop" type="button" aria-label="关闭 Task 摘要" @click="summaryOpen = false" />
      <section v-if="summaryOpen && currentTask" class="summary-popover" role="dialog" aria-label="Task 摘要">
        <header class="summary-heading">
          <strong>Task 摘要</strong>
          <span class="summary-status" :class="currentTask.summary_status">{{ currentTask.summary_status }}<span v-if="currentTask.summary_final"> · final</span></span>
          <button class="summary-close" type="button" title="关闭摘要" @click="summaryOpen = false"><i class="bi bi-x-lg" /></button>
        </header>
        <p v-if="currentTask.summary_preview" class="summary-text">{{ currentTask.summary_preview }}</p>
        <p v-else class="summary-empty">暂无摘要。</p>
        <button v-if="currentTask.summary_status === 'failed' || currentTask.summary_status === 'missing'" class="summary-retry" type="button" @click="retrySummary">重试摘要</button>
      </section>
      <div ref="thread" class="timeline" @scroll.passive="onScroll">
        <button v-if="hasOlder" class="page-button" type="button" @click="loadOlder">加载更早消息</button><div v-if="loading && messages.length === 0" class="empty-state">加载中…</div>
        <article v-for="group in timeline" :key="group.id" class="turn-box" :class="{ user: group.user }" :data-message-id="groupAnchorId(group)">
          <header class="turn-header">
            <strong>{{ group.user ? 'User' : group.roleName }}</strong>
            <span v-for="entry in queuedFor(group.turnId)" :key="entry.dispatch_id" class="queued-meta">
              <i class="bi bi-hourglass-split" aria-hidden="true" />
              <span>排队 #{{ entry.position }}</span>
              <button class="queued-action" type="button" title="编辑排队消息" aria-label="编辑排队消息" @click="editQueued(entry)"><i class="bi bi-pencil" /></button>
              <button class="queued-action queued-cancel" type="button" title="取消排队消息" aria-label="取消排队消息" @click="cancelQueued(entry)"><i class="bi bi-x-lg" /></button>
            </span>
            <span v-if="!group.user && roleTarget(group.turnId)" class="turn-target">{{ roleTarget(group.turnId) }}</span>
            <span v-if="!group.user && running.has(group.turnId)" class="turn-running"><i class="bi bi-arrow-repeat spin" /> running</span>
            <time>{{ formatTime(group.at) }}</time>
            <button v-if="group.user" class="cite-button" type="button" title="引用消息" @click="citeMessage(group.user.id)"><i class="bi bi-quote" /></button>
            <button v-if="!group.user && running.has(group.turnId)" class="turn-stop-button" type="button" title="停止 Turn" @click="stopTurn(group.turnId)"><i class="bi bi-stop-circle" /></button>
          </header>
          <div class="turn-body">
          <TurnContent :items="group.items" :decisions="decisions" />
          <DecisionCard v-for="decision in group.decisions" :key="decision.id" :decision="decision" :draft="responseDrafts[decision.id] ?? ''" @update:draft="responseDrafts[decision.id] = $event" @respond="respondDecision" @cancel="cancelDecision(decision)" @open="openDecisionSource(decision)" />
          </div>
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
.task-chat{display:flex;flex-direction:column;height:100%;min-height:0;overflow:hidden;position:relative;color:var(--color-text)}.spin{display:inline-block;animation:spin 1s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}.rename-row,.error-row{align-items:center;display:flex;gap:5px;padding:5px 8px}.rename-row input{background:var(--color-surface);border:1px solid var(--color-border);color:var(--color-text);flex:1;min-width:0;padding:4px 6px}.error-row{color:var(--bs-danger);font-size:var(--font-size-ui-small)}.error-row button{margin-left:auto}.task-body{display:flex;flex:1;flex-direction:column;min-height:0;overflow:hidden}.timeline{flex:1;min-height:0;overflow-y:auto;padding:8px}.task-composer{border-top:1px solid var(--color-border);flex:0 0 auto;max-height:65vh;overflow:visible;position:relative;z-index:20}
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
.composer-overlay-dot { background: var(--color-accent); border-radius: 50%; height: 6px; position: absolute; right: 4px; top: 4px; width: 6px; }.page-button{color:var(--color-text-muted);display:block;margin:4px auto 10px}.empty-state,.ended-banner{color:var(--color-text-muted);padding:20px;text-align:center}.ended-banner{border-top:1px solid var(--color-border);padding:8px}
/* Task summary is an overlay, never a full-width row in the reading flow. */
.task-body { position: relative; }
.summary-trigger {
  align-items: center;
  backdrop-filter: blur(6px);
  background: color-mix(in srgb, var(--color-surface-raised) 78%, transparent);
  border: 1px solid color-mix(in srgb, var(--color-border) 72%, transparent);
  border-radius: 999px;
  color: var(--color-text-muted);
  display: inline-flex;
  height: 28px;
  justify-content: center;
  left: 8px;
  opacity: .78;
  padding: 0;
  position: absolute;
  top: 8px;
  width: 28px;
  z-index: 12;
}
.summary-trigger:hover,
.summary-trigger.active { color: var(--color-accent); opacity: 1; }
.summary-backdrop {
  background: transparent;
  border: 0;
  inset: 0;
  padding: 0;
  position: absolute;
  z-index: 10;
}
.summary-popover {
  background: var(--color-surface-raised);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 8px 28px color-mix(in srgb, var(--bs-body-color) 18%, transparent);
  left: 8px;
  max-height: min(60vh, calc(100% - 48px));
  overflow: auto;
  padding: 10px 12px;
  position: absolute;
  top: 42px;
  width: min(520px, calc(100% - 16px));
  z-index: 11;
}
.summary-heading { align-items: center; display: flex; font-size: var(--font-size-ui); gap: 8px; }
.summary-status { color: var(--color-text-muted); font-size: var(--font-size-ui-small); margin-left: auto; }
.summary-status.completed,
.summary-status.pending { color: var(--bs-primary); }
.summary-status.failed { color: var(--bs-danger); }
.summary-close,
.summary-retry {
  background: transparent;
  border: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-ui-small);
  padding: 2px 4px;
}
.summary-close:hover,
.summary-retry:hover { color: var(--color-text); }
.summary-retry { border: 1px solid var(--color-border); border-radius: var(--radius-sm); padding: 3px 8px; }
.summary-text,
.summary-empty { font-size: var(--font-size-ui); line-height: 1.5; margin: 8px 0; white-space: pre-wrap; }
.summary-empty { color: var(--color-text-muted); }

/* Old Viewer turn styling: quiet metadata, one muted surface per Turn, and
   compact content typography. Activity rows remain boxless inside it. */
.turn-box { border: 0; margin: 0 auto 8px; min-width: 0; padding: 0; }
.turn-header {
  align-items: center;
  color: var(--color-text-muted);
  display: flex;
  font-size: var(--font-size-ui);
  gap: 6px;
  margin: 0;
  min-width: 0;
  overflow: hidden;
  padding: 0 2px 2px;
  white-space: nowrap;
}
.turn-header strong { color: inherit; flex: 0 0 auto; font-weight: 700; }
.turn-target {
  color: color-mix(in srgb, var(--color-text-muted) 72%, transparent);
  flex: 0 1 auto;
  font-size: calc(var(--font-size-ui) * 5 / 6);
  line-height: 1.2;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
.turn-header time { color: var(--color-text-muted); flex: 0 0 auto; font-size: var(--font-size-ui-small); line-height: 1.2; margin-left: auto; }
.turn-running { align-items: center; color: var(--bs-primary); display: inline-flex; flex: 0 0 auto; font-size: calc(var(--font-size-ui) * 7 / 8); gap: 4px; }
.cite-button,
.turn-stop-button { background: none; border: 0; font-size: var(--font-size-ui); line-height: 1; padding: 0 2px; }
.cite-button { color: var(--color-text-muted); }
.cite-button:hover { color: var(--bs-primary); }
.turn-stop-button { color: var(--bs-danger); }
.turn-stop-button:hover { color: var(--bs-danger-text-emphasis, var(--bs-danger)); }
.turn-body {
  background: var(--color-surface-muted);
  border: 0;
  border-radius: var(--radius-md);
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  overflow-x: hidden;
  padding: 8px 10px;
  user-select: text;
  width: 100%;
}
.queued-meta { align-items: center; color: var(--color-text-muted); display: inline-flex; flex: 0 0 auto; font-size: var(--font-size-ui-small); gap: 4px; line-height: 1; }
.queued-action { background: transparent; border: 0; color: var(--color-text-muted); font-size: var(--font-size-ui-small); line-height: 1; padding: 0 2px; }
.queued-action:hover { color: var(--color-text); }
.queued-cancel:hover { color: var(--bs-danger); }
@media (max-width: 700px) {
  .turn-header > .turn-running { display: inline-flex; }
  .turn-header > .turn-target { display: none; }
}
</style>
