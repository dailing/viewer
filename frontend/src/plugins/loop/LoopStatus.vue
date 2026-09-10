<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, ref } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { Branch, Role } from "../chat/types";
import { errorText } from "../chat/types";
import type { LoopRun, LoopIteration } from "./types";

const props = defineProps<{ chatId: string; roles: Role[]; branches: Branch[]; fromTurnId: string }>();
const emit = defineEmits<{ select: [branchId: string, turn?: LoopIteration] }>();
const ctx = inject<PluginCtx>("pluginCtx")!;
const runs = ref<LoopRun[]>([]);
const chosen = ref("");
const run = computed(() => runs.value.find((r) => r.id === chosen.value) ?? runs.value[0]);
const iterations = ref<LoopIteration[]>([]);
const moreIterations = ref(false);
const moreRuns = ref(false);
// Opened via the chat pane chrome button: show the full loop panel (history
// + status) directly instead of just the collapsed summary bar.
const expanded = ref(true);
const creating = ref(false);
const busy = ref(false);
const recoveryArmed = ref(false);
const error = ref("");
const available = ref(true);
const goal = ref("");
const criteria = ref("");
const roleId = ref("");
const target = ref("new");
const maxIterations = ref(20);
const minutes = ref(60);
const turnMinutes = ref(15);
const judgeEvery = ref(3);
const createId = ref("");
const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | undefined;
let alive = true;
let fetching = false;
let refreshAgain = false;
const labels: Record<string, string> = { draft: "待启动", running: "执行中", paused: "已暂停", stopping: "停止中", limiting: "超时停止中", completed: "目标完成", stopped: "已停止", limited: "达到上限" };
const phaseLabels: Record<string, string> = { dispatching: "投递中", waiting: "等待执行结束", ready: "待判定", judging: "判定中", done: "已结束" };
const verdictLabels: Record<string, string> = { complete: "满足验收条件", continue: "继续", blocked: "等待用户", failed: "执行失败", cancelled: "已取消" };
const ended = computed(() => run.value !== undefined && ["completed", "stopped", "limited"].includes(run.value.state));
const remaining = computed(() => run.value?.deadline ? Math.max(0, Math.ceil((run.value.deadline - now.value) / 60_000)) : null);

async function refresh(): Promise<void> {
  if (fetching) { refreshAgain = true; return; }
  fetching = true;
  try {
    const result = await ctx.bus.request("loop:_:list", { chat_id: props.chatId }) as { loops: LoopRun[] };
    if (!alive) return;
    const incoming = result.loops ?? [];
    if (runs.value.length <= incoming.length) moreRuns.value = incoming.length === 20;
    runs.value = [...incoming, ...runs.value.filter((old) => !incoming.some((item) => item.id === old.id))];
    available.value = true;
    if (expanded.value && run.value) {
      const id = run.value.id;
      const detail = await ctx.bus.request("loop:_:get", { id }) as { loop: LoopRun; iterations: LoopIteration[]; has_more: boolean };
      if (alive && run.value?.id === id) { const latest = detail.iterations ?? [];
        const older = iterations.value.filter((old) => old.id.startsWith(`loop/${id}/`) && !latest.some((item) => item.id === old.id));
        iterations.value = [...latest, ...older];
        if (!older.length) moreIterations.value = detail.has_more;
        runs.value = runs.value.map((r) => r.id === id ? detail.loop : r); }
    }
  } catch (e) {
    if (alive) { available.value = false; if (expanded.value || creating.value) error.value = errorText(e); }
  } finally {
    fetching = false;
    if (refreshAgain && alive) { refreshAgain = false; void refresh(); }
  }
}
async function olderIterations(): Promise<void> {
  if (!run.value || !iterations.value.length || busy.value) return;
  busy.value = true;
  try {
    const id = run.value.id;
    const detail = await ctx.bus.request("loop:_:get", { id, before_iteration: iterations.value[iterations.value.length - 1].number }) as { iterations: LoopIteration[]; has_more: boolean };
    if (run.value?.id === id) { iterations.value.push(...detail.iterations); moreIterations.value = detail.has_more; }
  } catch (e) { error.value = errorText(e); } finally { busy.value = false; }
}
async function olderRuns(): Promise<void> {
  if (!runs.value.length || busy.value) return;
  busy.value = true;
  try {
    const result = await ctx.bus.request("loop:_:list", { chat_id: props.chatId, before_created_at: runs.value[runs.value.length - 1].created_at }) as { loops: LoopRun[] };
    runs.value.push(...result.loops); moreRuns.value = result.loops.length === 20;
  } catch (e) { error.value = errorText(e); } finally { busy.value = false; }
}
function openCreate(): void {
  creating.value = !creating.value;
  if (creating.value) { createId.value = crypto.randomUUID(); roleId.value = ""; target.value = "new"; error.value = ""; }
}
async function create(): Promise<void> {
  if (busy.value) return;
  busy.value = true; error.value = "";
  try {
    const created = await ctx.bus.request("loop:_:create", {
      id: createId.value, chat_id: props.chatId, role_id: roleId.value, goal: goal.value, criteria: criteria.value,
      new_branch: target.value === "new", branch_id: target.value === "main" || target.value === "new" ? "" : target.value,
      from_turn_id: target.value === "new" ? props.fromTurnId : "", max_iterations: maxIterations.value,
      duration_seconds: Math.round(minutes.value * 60), turn_timeout_seconds: Math.round(turnMinutes.value * 60), judge_every: judgeEvery.value,
    }) as LoopRun;
    chosen.value = created.id;
    // The explicit create-and-start submit authorizes starting this draft.
    await ctx.bus.request("loop:_:start", { id: created.id });
    creating.value = false; expanded.value = true; goal.value = ""; criteria.value = "";
    emit("select", created.branch_id);
  } catch (e) { error.value = errorText(e); }
  finally { busy.value = false; await refresh(); }
}
async function action(op: string, extend = false): Promise<void> {
  if (!run.value || busy.value) return;
  busy.value = true; error.value = "";
  try {
    await ctx.bus.request(`loop:_:${op}`, { id: run.value.id, ...(extend ? { extend_seconds: 3600 } : {}), ...(op === "recover" ? { confirmed_stopped: true } : {}) });
  } catch (e) { error.value = errorText(e); }
  finally { busy.value = false; await refresh(); }
}
function changed(frame: { value?: unknown }): void {
  if ((frame.value as { chat_id?: string } | undefined)?.chat_id === props.chatId) void refresh();
}
onMounted(() => {
  ctx.bus.subscribe("loop:_:changed", changed);
  void refresh();
  timer = setInterval(() => { now.value = Date.now(); void refresh(); }, 5000);
});
onBeforeUnmount(() => { alive = false; if (timer) clearInterval(timer); ctx.bus.unsubscribe("loop:_:changed", changed); });
</script>

<template>
  <div class="loop-panel">
    <div class="loop-bar">
      <button class="btn btn-sm btn-link" type="button" @click="expanded = !expanded; refresh()" :aria-expanded="expanded">
        <i class="bi bi-arrow-repeat" /> Loop<span v-if="run"> · {{ labels[run.state] ?? run.state }} · {{ run.iteration }}/{{ run.max_iterations }} 轮<span v-if="remaining !== null && !ended"> · 剩余 {{ remaining }} 分钟</span></span>
      </button>
      <span v-if="run" class="loop-goal" :title="run.goal">{{ run.goal }}</span>
      <button class="btn btn-sm btn-outline-secondary" type="button" @click="openCreate">{{ creating ? '收起' : '新建 Loop' }}</button>
    </div>
    <p v-if="!available && (expanded || creating)" class="small text-secondary m-2">Loop 插件暂不可用，正在等待连接。</p>
    <p v-if="error" role="alert" class="small text-danger m-2">{{ error }}</p>
    <form v-if="creating" class="loop-form" @submit.prevent="create">
      <label>目标<textarea v-model="goal" required maxlength="8000" rows="2" class="form-control form-control-sm" placeholder="这次循环要完成什么" /></label>
      <label>验收条件<textarea v-model="criteria" required maxlength="8000" rows="2" class="form-control form-control-sm" placeholder="哪些结果和验证能证明已经完成" /></label>
      <div class="loop-fields">
        <label>执行角色<select v-model="roleId" required class="form-select form-select-sm"><option disabled value="">请选择角色</option><option v-for="role in roles" :key="role.id" :value="role.id">{{ role.name }}</option></select></label>
        <label>执行分支<select v-model="target" class="form-select form-select-sm"><option value="new">新建专用分支</option><option value="main">主线</option><option v-for="branch in branches.filter((b) => !b.archived_at)" :key="branch.id" :value="branch.id">{{ branch.name }}</option></select></label>
        <label>最多轮数<input v-model.number="maxIterations" type="number" min="1" max="1000" required class="form-control form-control-sm" /></label>
        <label>总时限（分钟）<input v-model.number="minutes" type="number" min="1" max="1440" required class="form-control form-control-sm" /></label>
        <label>单轮时限（分钟）<input v-model.number="turnMinutes" type="number" min="1" max="1440" required class="form-control form-control-sm" /></label>
        <label>每几轮检查进展<input v-model.number="judgeEvery" type="number" min="1" max="100" required class="form-control form-control-sm" /></label>
      </div>
      <p class="small text-secondary mb-1">关闭页面后继续运行。在执行分支发消息会暂停自动续轮；暂停期间总时限继续计时。完成声明始终经过验收判定。</p>
      <button class="btn btn-sm btn-primary" type="submit" :disabled="busy || !roleId">{{ busy ? '处理中…' : '创建并启动' }}</button>
    </form>
    <p v-if="expanded && available && !run && !creating" class="small text-secondary m-2">还没有 Loop 记录。点击「新建 Loop」开始一次循环。</p>
    <div v-if="expanded && run" class="loop-detail">
      <select v-if="runs.length > 1" :value="run.id" class="form-select form-select-sm mb-2" aria-label="选择 Loop" @change="chosen = ($event.target as HTMLSelectElement).value; iterations = []; refresh()"><option v-for="item in runs" :key="item.id" :value="item.id">{{ labels[item.state] }} · {{ item.goal }}</option></select>
      <button v-if="moreRuns" class="btn btn-sm btn-link mb-1" :disabled="busy" @click="olderRuns">加载更早的 Loop</button>
      <p class="mb-1"><strong>{{ run.goal }}</strong> · {{ run.role_name }}</p>
      <p class="loop-text small mb-1">验收条件：{{ run.criteria }}</p>
      <p v-if="run.reason" class="loop-text small mb-1">{{ run.reason }}</p>
      <p class="small text-secondary mb-2 loop-path">进度文件：{{ run.progress_path }}</p>
      <div class="d-flex flex-wrap gap-2 mb-2">
        <button class="btn btn-sm btn-outline-secondary" type="button" @click="emit('select', run.branch_id)">查看执行分支</button>
        <button v-if="run.state === 'draft'" class="btn btn-sm btn-primary" :disabled="busy" @click="action('start')">启动</button>
        <button v-if="run.state === 'running'" class="btn btn-sm btn-outline-secondary" :disabled="busy" @click="action('pause')">暂停续轮</button>
        <button v-if="run.state === 'paused'" class="btn btn-sm btn-primary" :disabled="busy" @click="action('resume', remaining === 0)">{{ remaining === 0 ? '延长 60 分钟并恢复' : '恢复' }}</button>
        <button v-if="!ended && !['stopping', 'limiting'].includes(run.state)" class="btn btn-sm btn-outline-danger" :disabled="busy" @click="action('stop')">停止 Loop</button>
      </div>
      <div v-if="run.reason.includes('状态不明')" class="small mb-2">
        <p>请先确认旧 agent 已停止，再将该轮标记为中断。</p>
        <button class="btn btn-sm btn-outline-warning" :disabled="busy" @click="recoveryArmed ? action('recover') : (recoveryArmed = true)">{{ recoveryArmed ? '确认已停止，标记中断' : '处理恢复状态' }}</button>
      </div>
      <p v-if="!iterations.length" class="small text-secondary">尚无迭代记录。</p>
      <details v-for="iteration in iterations" :key="iteration.id" class="loop-iteration">
        <summary>第 {{ iteration.number }} 轮 · {{ verdictLabels[iteration.verdict] ?? phaseLabels[iteration.state] ?? iteration.state }}<span v-if="iteration.checkpoint_error"> · 检查点异常</span></summary>
        <p class="loop-text small mt-1 mb-1">{{ iteration.feedback || '等待本轮结束' }}</p>
        <p v-if="iteration.checkpoint_error" class="small text-warning">{{ iteration.checkpoint_error }}</p>
        <button v-if="iteration.turn_id" class="btn btn-sm btn-link" @click="emit('select', run.branch_id, iteration)">查看执行记录</button>
      </details>
      <button v-if="moreIterations" class="btn btn-sm btn-link" :disabled="busy" @click="olderIterations">加载更早的迭代</button>
    </div>
  </div>
</template>

<style scoped>
.loop-panel { flex: 0 0 auto; border-bottom: 1px solid var(--color-border, #8884); font-size: .85rem; }
.loop-bar { display: flex; align-items: center; gap: .4rem; padding: .25rem .5rem; }
.loop-bar > button { flex-shrink: 0; }
.loop-goal { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; min-width: 0; }
.loop-form, .loop-detail { padding: .5rem .75rem; max-height: 50vh; overflow: auto; }
.loop-form > label { display: block; margin-bottom: .5rem; }
.loop-fields { display: grid; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); gap: .5rem; margin-bottom: .5rem; }
.loop-text { white-space: pre-wrap; overflow-wrap: anywhere; }
.loop-path { overflow-wrap: anywhere; }
.loop-iteration { padding: .35rem 0; border-top: 1px solid var(--color-border, #8884); }
@media (max-width: 600px) { .loop-bar { flex-wrap: wrap; } .loop-goal { display: none; } .loop-bar > button:first-child { flex: 1; text-align: left; white-space: normal; } }
</style>
