<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useAgentTasksStore } from "../stores/agentTasks";
import type { AgentTask, AgentTaskStatus } from "../types/agentTasks";

const taskStore = useAgentTasksStore();
const busy = ref(false);
const error = ref("");
const groupDraft = ref("default");
const editTitle = ref("");
const editDescription = ref("");
const editInstruction = ref("");
const editWorkspace = ref("");

const statuses: AgentTaskStatus[] = ["backlog", "ready", "running", "waiting_process", "review", "blocked", "done", "failed"];
const selectedTask = computed(() => taskStore.selectedTask);
const tasksByStatus = computed(() => {
  const groups: Record<string, AgentTask[]> = {};
  for (const status of statuses) groups[status] = [];
  for (const task of taskStore.tasks) {
    if (!groups[task.status]) groups[task.status] = [];
    groups[task.status].push(task);
  }
  return groups;
});
const roots = computed(() => taskStore.tasks.filter((task) => task.parent_id === null || !task.parent_id));
const childrenByParent = computed(() => {
  const groups: Record<string, AgentTask[]> = {};
  for (const task of taskStore.tasks) {
    if (!task.parent_id) continue;
    groups[task.parent_id] = [...(groups[task.parent_id] ?? []), task];
  }
  return groups;
});
const selectedDependencies = computed(() => selectedTask.value?.depends_on.map((id) => taskStore.byId[id]).filter(Boolean) ?? []);
const mode = computed(() => taskStore.settings?.mode ?? "manual");

onMounted(async () => {
  await load();
});

watch(selectedTask, (task) => syncEdit(task), { immediate: true });

async function load() {
  await withBusy(async () => {
    await taskStore.load(groupDraft.value || "default");
    groupDraft.value = taskStore.selectedGroupId;
    if (taskStore.selectedId) await taskStore.loadContext(taskStore.selectedId);
  });
}

function syncEdit(task: AgentTask | null) {
  editTitle.value = task?.title ?? "";
  editDescription.value = task?.description ?? "";
  editInstruction.value = task?.execution.instruction ?? "";
  editWorkspace.value = task?.workspace ?? "";
}

function formatTs(value?: number | null) {
  if (!value) return "n/a";
  return new Intl.DateTimeFormat(undefined, {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value * 1000));
}

function statusClass(status: string) {
  return `task-status-${status.replace("_", "-")}`;
}

async function selectTask(id: string) {
  taskStore.selectedId = id;
  await withBusy(async () => {
    await taskStore.loadContext(id);
  });
}

async function createTask() {
  const title = window.prompt("Task title", "New Agent Task");
  if (!title) return;
  await withBusy(async () => {
    await taskStore.create({
      title,
      group_id: groupDraft.value || "default",
      status: "backlog",
      priority: 50,
      assigned_agent: taskStore.settings?.default_agent ?? "codex",
      execution: {
        mode: "agent",
        instruction: title,
        cwd: "",
        env: {},
      },
    });
  });
}

async function saveSelected() {
  const task = selectedTask.value;
  if (!task) return;
  await withBusy(async () => {
    await taskStore.patch(task.id, {
      expected_version: task.version,
      title: editTitle.value,
      description: editDescription.value,
      workspace: editWorkspace.value,
      execution: {
        ...task.execution,
        instruction: editInstruction.value,
      },
      reason: "Updated from task DAG page",
    });
  });
}

async function addDependency() {
  const task = selectedTask.value;
  if (!task) return;
  const depId = window.prompt("Dependency task id");
  if (!depId) return;
  await withBusy(async () => {
    await taskStore.patchDependencies(task.id, {
      add: [depId],
      expected_version: task.version,
      reason: "Added from task DAG page",
    });
  });
}

async function removeDependency(depId: string) {
  const task = selectedTask.value;
  if (!task) return;
  await withBusy(async () => {
    await taskStore.patchDependencies(task.id, {
      remove: [depId],
      expected_version: task.version,
      reason: "Removed from task DAG page",
    });
  });
}

async function dispatchSelected(force = false) {
  const task = selectedTask.value;
  if (!task) return;
  await withBusy(async () => {
    await taskStore.dispatch(task.id, force);
  });
}

async function dispatchOneReady() {
  await withBusy(async () => {
    await taskStore.dispatchReady(true);
    await taskStore.load(groupDraft.value || "default");
  });
}

async function toggleMode() {
  await withBusy(async () => {
    await taskStore.saveSettings({
      default_group_id: groupDraft.value || "default",
      mode: mode.value === "auto" ? "manual" : "auto",
      default_agent: taskStore.settings?.default_agent ?? "codex",
      default_model: taskStore.settings?.default_model,
      auto_tick_seconds: taskStore.settings?.auto_tick_seconds ?? 10,
    });
  });
}

async function withBusy(action: () => Promise<void>) {
  busy.value = true;
  error.value = "";
  try {
    await action();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="agent-task-page">
    <aside class="task-left">
      <header class="task-panel-header">
        <div>
          <h2>Task DAG</h2>
          <span>{{ taskStore.tasks.length }} tasks</span>
        </div>
        <button class="btn btn-primary icon-button" type="button" title="New task" @click="createTask">
          <i class="bi bi-plus-lg"></i>
        </button>
      </header>

      <div class="task-group-row">
        <input v-model="groupDraft" class="form-control form-control-sm" type="text" placeholder="group_id" />
        <button class="btn btn-outline-secondary icon-button" type="button" title="Load group" @click="load">
          <i class="bi bi-arrow-clockwise"></i>
        </button>
      </div>

      <div class="task-mode-row">
        <button class="btn btn-outline-secondary" :class="{ active: mode === 'manual' }" type="button" @click="mode === 'auto' && toggleMode()">
          Manual
        </button>
        <button class="btn btn-outline-secondary" :class="{ active: mode === 'auto' }" type="button" @click="mode === 'manual' && toggleMode()">
          Auto
        </button>
        <button class="btn btn-outline-primary" type="button" :disabled="busy" @click="dispatchOneReady">Run Ready</button>
      </div>

      <div class="task-tree">
        <template v-for="root in roots" :key="root.id">
          <button class="task-tree-node" :class="{ active: root.id === taskStore.selectedId }" type="button" @click="selectTask(root.id)">
            <span class="task-status-pill" :class="statusClass(root.status)">{{ root.status }}</span>
            <span>{{ root.title }}</span>
          </button>
          <button
            v-for="child in childrenByParent[root.id] ?? []"
            :key="child.id"
            class="task-tree-node child"
            :class="{ active: child.id === taskStore.selectedId }"
            type="button"
            @click="selectTask(child.id)"
          >
            <span class="task-status-pill" :class="statusClass(child.status)">{{ child.status }}</span>
            <span>{{ child.title }}</span>
          </button>
        </template>
        <div v-if="!taskStore.tasks.length" class="task-empty">No tasks in this group.</div>
      </div>
    </aside>

    <main class="task-main">
      <div v-if="error" class="alert alert-danger">{{ error }}</div>

      <section class="task-board">
        <article v-for="status in statuses" :key="status" class="task-column">
          <header>
            <h3>{{ status }}</h3>
            <span>{{ tasksByStatus[status]?.length ?? 0 }}</span>
          </header>
          <button
            v-for="task in tasksByStatus[status]"
            :key="task.id"
            class="task-card"
            :class="{ active: task.id === taskStore.selectedId }"
            type="button"
            @click="selectTask(task.id)"
          >
            <span class="task-card-title">{{ task.title }}</span>
            <span class="task-card-meta">p{{ task.priority }} · v{{ task.version }}</span>
          </button>
        </article>
      </section>

      <section v-if="selectedTask" class="task-detail">
        <header class="task-detail-header">
          <div>
            <h1>{{ selectedTask.title }}</h1>
            <span>{{ selectedTask.id }} · {{ selectedTask.group_id }} · updated {{ formatTs(selectedTask.updated_at) }}</span>
          </div>
          <div class="task-detail-actions">
            <button class="btn btn-outline-secondary" type="button" :disabled="busy" @click="dispatchSelected(true)">Start</button>
            <button class="btn btn-primary" type="button" :disabled="busy" @click="saveSelected">Save</button>
          </div>
        </header>

        <div class="task-form-grid">
          <label>
            <span>Title</span>
            <input v-model="editTitle" class="form-control" type="text" />
          </label>
          <label>
            <span>Workspace</span>
            <input v-model="editWorkspace" class="form-control" type="text" placeholder="served-root relative path or absolute path" />
          </label>
          <label class="wide">
            <span>Description</span>
            <textarea v-model="editDescription" class="form-control" rows="3"></textarea>
          </label>
          <label class="wide">
            <span>Agent Instruction</span>
            <textarea v-model="editInstruction" class="form-control" rows="5"></textarea>
          </label>
        </div>

        <div class="task-subgrid">
          <section>
            <header class="task-section-header">
              <h3>Dependencies</h3>
              <button class="btn btn-outline-secondary icon-button" type="button" title="Add dependency" @click="addDependency">
                <i class="bi bi-plus-lg"></i>
              </button>
            </header>
            <div v-for="dep in selectedDependencies" :key="dep.id" class="task-dependency">
              <span>{{ dep.title }}</span>
              <span class="task-status-pill" :class="statusClass(dep.status)">{{ dep.status }}</span>
              <button class="btn btn-outline-danger icon-button" type="button" title="Remove dependency" @click="removeDependency(dep.id)">
                <i class="bi bi-x-lg"></i>
              </button>
            </div>
            <div v-if="!selectedDependencies.length" class="task-empty small">No dependencies.</div>
          </section>

          <section>
            <h3>Runtime</h3>
            <dl class="task-runtime">
              <dt>Status</dt>
              <dd><span class="task-status-pill" :class="statusClass(selectedTask.status)">{{ selectedTask.status }}</span></dd>
              <dt>Agent</dt>
              <dd>{{ selectedTask.assigned_agent }} {{ selectedTask.agent_session_id || "" }}</dd>
              <dt>PID</dt>
              <dd>{{ selectedTask.runtime.pid ?? "n/a" }}</dd>
              <dt>Attempt</dt>
              <dd>{{ selectedTask.runtime.attempt }}</dd>
            </dl>
          </section>
        </div>

        <section class="task-result">
          <h3>Result</h3>
          <p>{{ selectedTask.result.summary || "No summary yet." }}</p>
          <div class="task-artifacts">
            <span v-for="artifact in selectedTask.artifacts" :key="`${artifact.type}:${artifact.path}`">{{ artifact.type }}: {{ artifact.path }}</span>
          </div>
        </section>
      </section>
    </main>
  </section>
</template>

<style scoped>
.agent-task-page {
  display: grid;
  grid-template-columns: minmax(260px, 320px) 1fr;
  height: 100%;
  min-height: 0;
  background: #f7f8fb;
}

.task-left {
  min-width: 0;
  border-right: 1px solid #d8dee8;
  background: #ffffff;
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
}

.task-panel-header,
.task-detail-header,
.task-section-header,
.task-column header,
.task-group-row,
.task-mode-row,
.task-detail-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.task-panel-header,
.task-detail-header,
.task-column header,
.task-section-header {
  justify-content: space-between;
}

h1,
h2,
h3 {
  margin: 0;
  letter-spacing: 0;
}

h1 {
  font-size: 19px;
}

h2 {
  font-size: 16px;
}

h3 {
  font-size: 13px;
}

.task-panel-header span,
.task-detail-header span,
.task-card-meta {
  color: #667085;
  font-size: 12px;
}

.task-mode-row .btn {
  flex: 1;
  min-width: 0;
}

.task-tree {
  min-height: 0;
  overflow: auto;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.task-tree-node,
.task-card {
  border: 1px solid #d8dee8;
  background: #ffffff;
  color: #172033;
  text-align: left;
  border-radius: 6px;
}

.task-tree-node {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 6px;
  align-items: center;
  padding: 7px;
}

.task-tree-node.child {
  margin-left: 16px;
}

.task-tree-node.active,
.task-card.active {
  border-color: #2f6fed;
  box-shadow: 0 0 0 1px #2f6fed inset;
}

.task-main {
  min-width: 0;
  min-height: 0;
  overflow: auto;
  padding: 12px;
}

.task-board {
  display: grid;
  grid-template-columns: repeat(8, minmax(150px, 1fr));
  gap: 8px;
  min-width: 1120px;
}

.task-column {
  background: #eef2f7;
  border: 1px solid #d8dee8;
  border-radius: 6px;
  padding: 8px;
  min-height: 160px;
}

.task-card {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 8px;
  margin-top: 8px;
}

.task-card-title {
  font-size: 13px;
  line-height: 1.25;
}

.task-detail {
  margin-top: 12px;
  background: #ffffff;
  border: 1px solid #d8dee8;
  border-radius: 6px;
  padding: 14px;
}

.task-form-grid,
.task-subgrid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
  margin-top: 12px;
}

.wide {
  grid-column: 1 / -1;
}

label span {
  display: block;
  font-size: 12px;
  color: #667085;
  margin-bottom: 4px;
}

.task-dependency {
  display: grid;
  grid-template-columns: 1fr auto auto;
  gap: 8px;
  align-items: center;
  padding: 7px 0;
  border-bottom: 1px solid #edf0f5;
}

.task-runtime {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 6px 10px;
  margin: 8px 0 0;
  font-size: 13px;
}

.task-runtime dt {
  color: #667085;
}

.task-runtime dd {
  margin: 0;
}

.task-status-pill {
  display: inline-flex;
  align-items: center;
  min-height: 20px;
  padding: 0 6px;
  border-radius: 999px;
  font-size: 11px;
  background: #e7ebf2;
  color: #344054;
}

.task-status-ready,
.task-status-done {
  background: #dcfce7;
  color: #166534;
}

.task-status-running,
.task-status-waiting-process {
  background: #dbeafe;
  color: #1d4ed8;
}

.task-status-review,
.task-status-blocked {
  background: #fef3c7;
  color: #92400e;
}

.task-status-failed,
.task-status-cancelled {
  background: #fee2e2;
  color: #b91c1c;
}

.task-empty {
  color: #667085;
  font-size: 13px;
  padding: 12px;
}

.task-empty.small {
  padding: 8px 0;
}

.task-result {
  margin-top: 12px;
}

.task-artifacts {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.task-artifacts span {
  background: #eef2f7;
  border-radius: 6px;
  padding: 4px 6px;
  font-size: 12px;
}

@media (max-width: 900px) {
  .agent-task-page {
    grid-template-columns: 1fr;
  }

  .task-left {
    max-height: 42vh;
    border-right: 0;
    border-bottom: 1px solid #d8dee8;
  }

  .task-form-grid,
  .task-subgrid {
    grid-template-columns: 1fr;
  }
}
</style>
