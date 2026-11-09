<script setup lang="ts">
import { inject, onBeforeUnmount, onMounted, ref } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import { ensureTaskView } from "./viewInstances";
import { formatChatTime as formatTime } from "./chatTime";
import type { ChatList, Task, TaskList } from "./types";
import { errorText } from "./types";

const injected = inject<PluginCtx>("pluginCtx");
if (!injected) throw new Error("TasksPage requires PluginPaneHost");
const ctx = injected;
const tasks = ref<Task[]>([]);
const chats = ref<ChatList["chats"]>([]);
const search = ref("");
const chatId = ref("");
const status = ref("");
const running = ref("");
const pinned = ref(false);
const summaryStatus = ref("");
const decisionStatus = ref("");
const loading = ref(false);
const error = ref("");
const hasMore = ref(false);
const cursor = ref<{ updated: number; id: string } | null>(null);

async function load(append = false): Promise<void> {
  loading.value = true;
  error.value = "";
  try {
    const request: Record<string, unknown> = { search: search.value, chat_id: chatId.value, status: status.value, summary_status: summaryStatus.value, decision_status: decisionStatus.value, limit: 50 };
    if (pinned.value) request.pinned = true;
    if (running.value) request.running = running.value === "running";
    if (append && cursor.value) { request.before_updated_at = cursor.value.updated; request.before_id = cursor.value.id; }
    const result = await ctx.bus.request("chat:_:tasks:list", request) as TaskList;
    tasks.value = append ? [...tasks.value, ...result.tasks] : result.tasks;
    hasMore.value = result.has_more === true;
    cursor.value = result.next_updated_at && result.next_id ? { updated: result.next_updated_at, id: result.next_id } : null;
  } catch (cause) { error.value = errorText(cause); }
  finally { loading.value = false; }
}
function openTask(task: Task): void {
  void ctx.shell.open({ paneType: "chat", instanceId: ensureTaskView(task.chat_id, task.id) });
}
function refresh(): void { void load(); }
onMounted(async () => {
  ctx.setChrome({ title: "Tasks" });
  ctx.bus.subscribe("chat:_:task", refresh);
  ctx.bus.subscribe("chat:_:task-summary", refresh);
  try { chats.value = (await ctx.bus.request("chat:_:chats:list", {}) as ChatList).chats; } catch (cause) { error.value = errorText(cause); }
  await load();
});
onBeforeUnmount(() => { ctx.bus.unsubscribe("chat:_:task", refresh); ctx.bus.unsubscribe("chat:_:task-summary", refresh); });
</script>

<template>
  <div class="tasks-page">
    <header class="tasks-filters">
      <input v-model="search" placeholder="搜索标题或摘要" @keyup.enter="load()">
      <select v-model="chatId" @change="load()"><option value="">全部 Chats</option><option v-for="chat in chats" :key="chat.id" :value="chat.id">{{ chat.name }}</option></select>
      <select v-model="status" @change="load()"><option value="">Open + Ended</option><option value="open">Open</option><option value="ended">Ended</option></select>
      <select v-model="running" @change="load()"><option value="">Running: 全部</option><option value="running">Running</option><option value="idle">Not running</option></select>
      <select v-model="summaryStatus" @change="load()"><option value="">摘要: 全部</option><option value="completed">Completed</option><option value="pending">Pending</option><option value="failed">Failed</option><option value="missing">Missing</option></select>
      <select v-model="decisionStatus" @change="load()"><option value="">Decision: 全部</option><option value="pending">Pending</option><option value="none">None</option></select>
      <label title="只看固定 Task"><input v-model="pinned" type="checkbox" @change="load()"> Pinned</label>
      <button type="button" @click="load()">查询</button>
    </header>
    <div v-if="error" class="tasks-error">{{ error }}</div>
    <div class="tasks-table">
      <div class="tasks-row tasks-heading"><span>Task / Chat</span><span>状态</span><span>摘要</span><span>Decision</span><span>更新时间</span></div>
      <button v-for="task in tasks" :key="task.id" class="tasks-row" type="button" @click="openTask(task)">
        <span class="task-name"><strong><i v-if="task.pinned" class="bi bi-pin-angle-fill" /> {{ task.title }}</strong><small>{{ task.chat_name }}</small></span>
        <span><i v-if="task.running" class="bi bi-arrow-repeat spin" /> {{ task.running ? "running" : task.status }}</span>
        <span class="task-summary"><small>{{ task.summary_status }}<template v-if="task.summary_final"> · final</template></small>{{ task.summary_preview }}</span>
        <span>{{ task.pending_decisions || 0 }}</span><time>{{ formatTime(task.updated_at) }}</time>
      </button>
      <div v-if="!loading && tasks.length === 0" class="tasks-empty">没有匹配的 Task。</div>
    </div>
    <button v-if="hasMore" class="tasks-more" type="button" :disabled="loading" @click="load(true)">加载更多</button>
  </div>
</template>

<style scoped>
.tasks-page{display:flex;flex-direction:column;height:100%;min-height:0;padding:10px;color:var(--color-text)}.tasks-filters{display:flex;flex-wrap:wrap;gap:6px;flex:0 0 auto;margin-bottom:10px}.tasks-filters input,.tasks-filters select{background:var(--color-surface);border:1px solid var(--color-border);color:var(--color-text);padding:5px 7px}.tasks-filters>input{flex:1;min-width:180px}.tasks-filters label{align-items:center;color:var(--color-text-muted);display:flex;font-size:var(--font-size-ui-small);gap:3px}.tasks-error{color:var(--bs-danger);padding:6px}.tasks-table{flex:1;min-height:0;overflow:auto}.tasks-row{align-items:center;background:transparent;border:0;border-bottom:1px solid var(--color-border);color:var(--color-text);display:grid;gap:12px;grid-template-columns:minmax(180px,1.2fr) 100px minmax(240px,2fr) 75px 110px;padding:8px;text-align:left;width:100%}button.tasks-row:hover{background:var(--color-surface-hover)}.tasks-heading{color:var(--color-text-muted);font-size:var(--font-size-ui-small);position:sticky;top:0;background:var(--color-surface);z-index:1}.task-name strong,.task-name small,.task-summary small{display:block}.task-name small,.task-summary,.tasks-row time{color:var(--color-text-muted)}.task-summary{font-size:var(--font-size-ui-small);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.tasks-empty,.tasks-more{color:var(--color-text-muted);margin:16px auto;text-align:center}.spin{display:inline-block;animation:spin 1s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}@media(max-width:760px){.tasks-heading{display:none}.tasks-row{grid-template-columns:1fr auto;margin:6px 0;padding:10px}.tasks-row .task-summary{grid-column:1/-1;white-space:normal;max-height:3em}.tasks-row>:nth-child(4){display:none}.tasks-row time{grid-column:1/-1}.tasks-filters select{max-width:48%}}
</style>
