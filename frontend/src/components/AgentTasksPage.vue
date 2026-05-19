<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { useAgentTasksStore } from "../stores/agentTasks";
import { renderMarkdown, renderMermaidIn } from "../utils/markdownRender";
import AgentViewer from "./viewers/AgentViewer.vue";
import type { AgentTask, AgentTaskStatus } from "../types/agentTasks";

const taskStore = useAgentTasksStore();
const busy = ref(false);
const error = ref("");
const notice = ref("");
const activePanel = ref<"groups" | "plan" | "tasks">("tasks");
const groupDraft = ref("default");
const editTitle = ref("");
const editDescription = ref("");
const editInstruction = ref("");
const editWorkspace = ref("");
const managerPrompt = ref("");
const sessionDialogOpen = ref(false);
const sessionDialogTitle = ref("");
const sessionDialogRef = ref("");
const planEditing = ref(false);
const planPreview = ref<HTMLElement | null>(null);
const planGoal = ref("");
const planText = ref("");
const planContext = ref("");
const planConstraints = ref("");

const statuses: AgentTaskStatus[] = ["backlog", "ready", "running", "waiting_process", "review", "blocked", "done", "failed"];
const dagNodeWidth = 230;
const dagNodeHeight = 92;
const dagColumnGap = 86;
const dagRowGap = 22;
const dagPadding = 28;
const selectedTask = computed(() => taskStore.selectedTask);
const statusCounts = computed(() => {
  const counts: Record<string, number> = {};
  for (const status of statuses) counts[status] = 0;
  for (const task of taskStore.tasks) counts[task.status] = (counts[task.status] ?? 0) + 1;
  return counts;
});
const selectedDependencies = computed(() => selectedTask.value?.depends_on.map((id) => taskStore.byId[id]).filter(Boolean) ?? []);
const mode = computed(() => taskStore.settings?.mode ?? "manual");
const managerRef = computed(() => (taskStore.plan?.manager_session_id ? `codex:${taskStore.plan.manager_session_id}` : ""));
const selectedTaskRef = computed(() => (selectedTask.value?.agent_session_id ? `${selectedTask.value.assigned_agent || "codex"}:${selectedTask.value.agent_session_id}` : ""));
const planPreviewMarkdown = computed(() =>
  [
    `# ${planGoal.value.trim() || "Global Plan"}`,
    planText.value.trim() ? `## Plan\n\n${planText.value.trim()}` : "",
    planContext.value.trim() ? `## Context\n\n${planContext.value.trim()}` : "",
    planConstraints.value.trim()
      ? `## Constraints\n\n${planConstraints.value
          .split(/\r?\n/)
          .map((item) => item.trim())
          .filter(Boolean)
          .map((item) => `- ${item}`)
          .join("\n")}`
      : "",
  ]
    .filter(Boolean)
    .join("\n\n"),
);
const planPreviewHtml = computed(() => renderMarkdown(planPreviewMarkdown.value));
const selectedDownstreamIds = computed(() => {
  const root = selectedTask.value;
  if (!root) return [] as string[];
  const dependents = new Map<string, string[]>();
  for (const task of taskStore.tasks) {
    for (const depId of task.depends_on) {
      dependents.set(depId, [...(dependents.get(depId) ?? []), task.id]);
    }
  }
  const affected: string[] = [];
  const seen = new Set<string>();
  const queue = [root.id];
  while (queue.length) {
    const current = queue.shift()!;
    if (seen.has(current)) continue;
    seen.add(current);
    affected.push(current);
    queue.push(...(dependents.get(current) ?? []));
  }
  return affected;
});
const dagLayout = computed(() => {
  const tasks = [...taskStore.tasks];
  const byId = new Map(tasks.map((task) => [task.id, task]));
  const indegree = new Map<string, number>();
  const outgoing = new Map<string, string[]>();
  const levelById = new Map<string, number>();
  const orderIndex = new Map(statuses.map((status, index) => [status, index]));

  for (const task of tasks) {
    indegree.set(task.id, 0);
    outgoing.set(task.id, []);
  }
  for (const task of tasks) {
    for (const depId of task.depends_on) {
      if (!byId.has(depId)) continue;
      indegree.set(task.id, (indegree.get(task.id) ?? 0) + 1);
      outgoing.set(depId, [...(outgoing.get(depId) ?? []), task.id]);
    }
  }

  const sortTasks = (items: AgentTask[]) =>
    items.sort((a, b) => {
      const statusDelta = (orderIndex.get(a.status) ?? 99) - (orderIndex.get(b.status) ?? 99);
      if (statusDelta) return statusDelta;
      if (b.priority !== a.priority) return b.priority - a.priority;
      return a.created_at - b.created_at;
    });

  const ready = sortTasks(tasks.filter((task) => (indegree.get(task.id) ?? 0) === 0));
  const ordered: AgentTask[] = [];
  while (ready.length) {
    const task = ready.shift()!;
    ordered.push(task);
    const nextIds = outgoing.get(task.id) ?? [];
    for (const nextId of nextIds) {
      const next = byId.get(nextId);
      if (!next) continue;
      const nextLevel = Math.max(levelById.get(nextId) ?? 0, (levelById.get(task.id) ?? 0) + 1);
      levelById.set(nextId, nextLevel);
      indegree.set(nextId, (indegree.get(nextId) ?? 0) - 1);
      if ((indegree.get(nextId) ?? 0) === 0) {
        ready.push(next);
        sortTasks(ready);
      }
    }
  }

  const orderedIds = new Set(ordered.map((task) => task.id));
  const cycleFallbackLevel = Math.max(0, ...Array.from(levelById.values())) + 1;
  for (const task of sortTasks(tasks.filter((item) => !orderedIds.has(item.id)))) {
    levelById.set(task.id, cycleFallbackLevel);
    ordered.push(task);
  }

  const columns = new Map<number, AgentTask[]>();
  for (const task of ordered) {
    const level = levelById.get(task.id) ?? 0;
    columns.set(level, [...(columns.get(level) ?? []), task]);
  }

  const positioned = new Map<string, { task: AgentTask; x: number; y: number; level: number }>();
  const levels = Array.from(columns.keys()).sort((a, b) => a - b);
  for (const level of levels) {
    const column = columns.get(level) ?? [];
    for (const [index, task] of column.entries()) {
      positioned.set(task.id, {
        task,
        level,
        x: dagPadding + level * (dagNodeWidth + dagColumnGap),
        y: dagPadding + index * (dagNodeHeight + dagRowGap),
      });
    }
  }

  const maxRows = Math.max(1, ...Array.from(columns.values()).map((column) => column.length));
  const width = dagPadding * 2 + Math.max(1, levels.length) * dagNodeWidth + Math.max(0, levels.length - 1) * dagColumnGap;
  const height = dagPadding * 2 + maxRows * dagNodeHeight + Math.max(0, maxRows - 1) * dagRowGap;
  const nodes = Array.from(positioned.values());
  const edges = tasks.flatMap((task) =>
    task.depends_on
      .map((depId) => {
        const source = positioned.get(depId);
        const target = positioned.get(task.id);
        if (!source || !target) return null;
        const sx = source.x + dagNodeWidth;
        const sy = source.y + dagNodeHeight / 2;
        const tx = target.x;
        const ty = target.y + dagNodeHeight / 2;
        const bend = Math.max(38, (tx - sx) / 2);
        return {
          id: `${depId}->${task.id}`,
          sourceId: depId,
          targetId: task.id,
          path: `M ${sx} ${sy} C ${sx + bend} ${sy}, ${tx - bend} ${ty}, ${tx} ${ty}`,
        };
      })
      .filter(Boolean),
  ) as { id: string; sourceId: string; targetId: string; path: string }[];
  return { nodes, edges, width, height };
});
const selectedEdgeIds = computed(() => {
  const task = selectedTask.value;
  if (!task) return new Set<string>();
  return new Set(dagLayout.value.edges.filter((edge) => edge.sourceId === task.id || edge.targetId === task.id).map((edge) => edge.id));
});

onMounted(async () => {
  await load();
  await renderPlanPreview();
});

watch(selectedTask, (task) => syncEdit(task), { immediate: true });
watch(planPreviewMarkdown, () => {
  if (!planEditing.value) void renderPlanPreview();
});
watch(planEditing, (editing) => {
  if (!editing) void renderPlanPreview();
});

async function load() {
  await withBusy(async () => {
    await taskStore.load(groupDraft.value || "default");
    groupDraft.value = taskStore.selectedGroupId;
    syncPlan();
    if (taskStore.selectedId) await taskStore.loadContext(taskStore.selectedId);
  });
}

async function selectGroup(groupId: string) {
  groupDraft.value = groupId;
  await load();
}

async function newGroup() {
  const groupId = window.prompt("New group id", "new_project");
  if (!groupId) return;
  const normalized = groupId.trim();
  if (!normalized) return;
  const goal = window.prompt("Group goal", "Describe the goal for this DAG") ?? "";
  groupDraft.value = normalized;
  await withBusy(async () => {
    await taskStore.savePlan({
      group_id: normalized,
      goal,
      plan: "",
      context: "",
      constraints: [
        "Executors may write inside their task-local workspace.",
        "Executors should not edit shared/common source code unless explicitly instructed.",
        "DAG changes and shared-code changes go through the manager.",
      ],
      reason: "Created group from Task DAG page",
    });
    await taskStore.load(normalized);
    syncPlan();
    notice.value = `Created group ${normalized}`;
  });
}

function syncPlan() {
  planGoal.value = taskStore.plan?.goal ?? "";
  planText.value = taskStore.plan?.plan ?? "";
  planContext.value = taskStore.plan?.context ?? "";
  planConstraints.value = (taskStore.plan?.constraints ?? []).join("\n");
  void renderPlanPreview();
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

async function savePlan() {
  await withBusy(async () => {
    await taskStore.savePlan({
      group_id: groupDraft.value || "default",
      goal: planGoal.value,
      plan: planText.value,
      context: planContext.value,
      constraints: planConstraints.value
        .split(/\r?\n/)
        .map((item) => item.trim())
        .filter(Boolean),
      reason: "Updated from task DAG page",
    });
    syncPlan();
    planEditing.value = false;
    await renderPlanPreview();
  });
}

async function renderPlanPreview() {
  await nextTick();
  await renderMermaidIn(planPreview.value, "dag-plan-mermaid");
}

async function askManager(trigger = "user") {
  if (!managerPrompt.value.trim()) return;
  await withBusy(async () => {
    const result = await taskStore.requestManager({
      group_id: groupDraft.value || "default",
      task_id: selectedTask.value?.id,
      prompt: managerPrompt.value,
      reason: "Requested from task DAG page",
      trigger,
    });
    notice.value = `Manager request sent to ${result.manager_session_id}`;
    managerPrompt.value = "";
    syncPlan();
    await openSessionOverlay(`codex:${result.manager_session_id}`, "Manager Session");
  });
}

async function deleteSelected() {
  const task = selectedTask.value;
  if (!task || !window.confirm(`Delete task "${task.title}"?`)) return;
  await withBusy(async () => {
    await taskStore.remove(task.id);
    notice.value = `Deleted ${task.id}`;
  });
}

async function openSessionOverlay(ref: string, title: string) {
  if (!ref) return;
  sessionDialogOpen.value = true;
  sessionDialogTitle.value = title;
  sessionDialogRef.value = ref;
}

function closeSessionOverlay() {
  sessionDialogOpen.value = false;
  sessionDialogRef.value = "";
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

async function resetSelected(action: "clear" | "retry") {
  const task = selectedTask.value;
  if (!task) return;
  const affectedCount = selectedDownstreamIds.value.length;
  const verb = action === "retry" ? "Retry" : "Clear";
  const detail =
    action === "retry"
      ? "This will clear stored runtime/output state for this task and all downstream tasks, then dispatch this task if ready."
      : "This will clear stored runtime/output state for this task and all downstream tasks.";
  if (!window.confirm(`${verb} "${task.title}" and ${affectedCount - 1} downstream task(s)?\n\n${detail}`)) return;
  await withBusy(async () => {
    const result = await taskStore.reset(task.id, action, `${verb} from task DAG page`);
    notice.value = `${verb} affected ${result.affected_task_ids.length} task(s)`;
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
  notice.value = "";
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
    <nav class="task-rail" aria-label="Task DAG panels">
      <button class="task-rail-button" :class="{ active: activePanel === 'groups' }" type="button" title="Groups" @click="activePanel = 'groups'">
        <i class="bi bi-folder"></i>
      </button>
      <button class="task-rail-button" :class="{ active: activePanel === 'plan' }" type="button" title="Global Plan" @click="activePanel = 'plan'">
        <i class="bi bi-diagram-3"></i>
      </button>
      <button class="task-rail-button" :class="{ active: activePanel === 'tasks' }" type="button" title="Tasks" @click="activePanel = 'tasks'">
        <i class="bi bi-kanban"></i>
      </button>
    </nav>

    <main class="task-main">
      <div v-if="error" class="alert alert-danger">{{ error }}</div>
      <div v-if="notice" class="alert alert-info">{{ notice }}</div>

      <section v-if="activePanel === 'groups'" class="task-panel">
        <header class="task-panel-header">
          <div>
            <h2>Groups</h2>
            <span>{{ taskStore.groupItems.length }} groups · {{ taskStore.tasks.length }} tasks loaded</span>
          </div>
          <button class="btn btn-primary icon-button" type="button" title="New group" @click="newGroup">
            <i class="bi bi-folder-plus"></i>
          </button>
        </header>

        <div class="task-group-row">
          <label class="group-select-label">
            <span>Current Group</span>
            <select v-model="groupDraft" class="form-select form-select-sm" @change="selectGroup(groupDraft)">
              <option v-for="group in taskStore.groups" :key="group" :value="group">{{ group }}</option>
            </select>
          </label>
          <button class="btn btn-outline-secondary icon-button" type="button" title="Load group" @click="load">
            <i class="bi bi-arrow-clockwise"></i>
          </button>
        </div>

        <section class="group-list">
          <button
            v-for="group in taskStore.groupItems"
            :key="group.group_id"
            class="group-list-item"
            :class="{ active: group.group_id === taskStore.selectedGroupId }"
            type="button"
            @click="selectGroup(group.group_id)"
          >
            <span class="group-title">{{ group.group_id }}</span>
            <span class="group-meta">{{ group.task_count }} tasks · {{ group.mode }}</span>
            <span class="group-goal">{{ group.goal || "No goal set" }}</span>
          </button>
        </section>
      </section>

      <section v-else-if="activePanel === 'plan'" class="task-panel">
        <section class="plan-editor">
          <header class="task-section-header">
            <div>
              <h2>Global Plan</h2>
              <span>{{ taskStore.selectedGroupId }}</span>
            </div>
            <div class="task-detail-actions">
              <button v-if="!planEditing" class="btn btn-outline-secondary" type="button" :disabled="busy" @click="planEditing = true">Edit</button>
              <button v-else class="btn btn-outline-secondary" type="button" :disabled="busy" @click="planEditing = false">Cancel</button>
              <button v-if="planEditing" class="btn btn-primary" type="button" :disabled="busy" @click="savePlan">Save Plan</button>
            </div>
          </header>
          <article
            v-if="!planEditing"
            ref="planPreview"
            class="markdown-body markdown-content plan-markdown-preview"
            v-html="planPreviewHtml"
          ></article>
          <div v-else class="task-form-grid">
            <label class="wide">
              <span>Goal</span>
              <input v-model="planGoal" class="form-control" type="text" placeholder="Fixed goal for this DAG" />
            </label>
            <label class="wide">
              <span>Plan</span>
              <textarea v-model="planText" class="form-control" rows="4"></textarea>
            </label>
            <label class="wide">
              <span>Context</span>
              <textarea v-model="planContext" class="form-control" rows="3"></textarea>
            </label>
            <label class="wide">
              <span>Constraints</span>
              <textarea v-model="planConstraints" class="form-control" rows="3" placeholder="One constraint per line"></textarea>
            </label>
          </div>
        </section>

        <section class="manager-box">
          <header class="task-section-header">
            <h3>Manager</h3>
            <button
              v-if="managerRef"
              class="btn btn-outline-secondary btn-sm"
              type="button"
              @click="openSessionOverlay(managerRef, 'Manager Session')"
            >
              Session {{ taskStore.plan?.manager_session_id?.slice(0, 8) }}
            </button>
          </header>
          <textarea
            v-model="managerPrompt"
            class="form-control form-control-sm"
            rows="5"
            placeholder="Ask Codex manager to plan, replan, debug, or reschedule this DAG"
          ></textarea>
          <button class="btn btn-primary" type="button" :disabled="busy || !managerPrompt.trim()" @click="askManager()">Ask Manager</button>
        </section>
      </section>

      <section v-else class="task-panel">
        <header class="task-panel-header">
          <div>
            <h2>Tasks</h2>
            <span>{{ taskStore.selectedGroupId }} · {{ taskStore.tasks.length }} tasks</span>
          </div>
          <div class="task-detail-actions">
            <button class="btn btn-outline-primary" type="button" :disabled="busy" @click="dispatchOneReady">Run Ready</button>
            <button class="btn btn-primary icon-button" type="button" title="New task" @click="createTask">
              <i class="bi bi-plus-lg"></i>
            </button>
          </div>
        </header>

        <div class="task-mode-row">
          <button class="btn btn-outline-secondary" :class="{ active: mode === 'manual' }" type="button" @click="mode === 'auto' && toggleMode()">
            Manual
          </button>
          <button class="btn btn-outline-secondary" :class="{ active: mode === 'auto' }" type="button" @click="mode === 'manual' && toggleMode()">
            Auto
          </button>
        </div>

        <section class="task-dag-shell">
          <div class="task-status-legend" aria-label="Task status counts">
            <span v-for="status in statuses" :key="status" class="task-legend-item">
              <i class="task-legend-dot" :class="statusClass(status)"></i>
              {{ status }} {{ statusCounts[status] ?? 0 }}
            </span>
          </div>

          <div class="task-dag-scroll">
            <div v-if="!taskStore.tasks.length" class="task-empty">No tasks in this group.</div>
            <div v-else class="task-dag-canvas" :style="{ width: `${dagLayout.width}px`, height: `${dagLayout.height}px` }">
              <svg class="task-dag-edges" :viewBox="`0 0 ${dagLayout.width} ${dagLayout.height}`" aria-hidden="true">
                <path
                  v-for="edge in dagLayout.edges"
                  :key="edge.id"
                  class="task-dag-edge"
                  :class="{ active: selectedEdgeIds.has(edge.id), dimmed: selectedTask && !selectedEdgeIds.has(edge.id) }"
                  :d="edge.path"
                />
              </svg>

              <button
                v-for="node in dagLayout.nodes"
                :key="node.task.id"
                class="task-dag-node"
                :class="[
                  statusClass(node.task.status),
                  {
                    active: node.task.id === taskStore.selectedId,
                    dimmed: selectedTask && node.task.id !== selectedTask.id && !selectedEdgeIds.has(`${node.task.id}->${selectedTask.id}`) && !selectedEdgeIds.has(`${selectedTask.id}->${node.task.id}`),
                  },
                ]"
                :style="{ left: `${node.x}px`, top: `${node.y}px`, width: `${dagNodeWidth}px`, height: `${dagNodeHeight}px` }"
                type="button"
                @click="selectTask(node.task.id)"
              >
                <span class="task-node-topline">
                  <span class="task-node-status">{{ node.task.status }}</span>
                  <span>p{{ node.task.priority }}</span>
                </span>
                <span class="task-node-title">{{ node.task.title }}</span>
                <span class="task-node-meta">{{ node.task.depends_on.length }} deps · v{{ node.task.version }}</span>
              </button>
            </div>
          </div>
        </section>

      <section v-if="selectedTask" class="task-detail">
        <header class="task-detail-header">
          <div>
            <h1>{{ selectedTask.title }}</h1>
            <span>{{ selectedTask.id }} · {{ selectedTask.group_id }} · updated {{ formatTs(selectedTask.updated_at) }}</span>
          </div>
          <div class="task-detail-actions">
            <button
              v-if="selectedTaskRef"
              class="btn btn-outline-secondary"
              type="button"
              @click="openSessionOverlay(selectedTaskRef, 'Task Session')"
            >
              Session
            </button>
            <button class="btn btn-outline-secondary" type="button" :disabled="busy" @click="dispatchSelected(true)">Start</button>
            <button class="btn btn-outline-secondary" type="button" :disabled="busy" @click="resetSelected('retry')">Retry</button>
            <button class="btn btn-outline-danger" type="button" :disabled="busy" @click="resetSelected('clear')">Clear</button>
            <button class="btn btn-primary" type="button" :disabled="busy" @click="saveSelected">Save</button>
            <button class="btn btn-outline-danger" type="button" :disabled="busy" @click="deleteSelected">Delete</button>
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
              <dt>Task Workspace</dt>
              <dd>{{ selectedTask.runtime.task_workspace ?? "created on dispatch" }}</dd>
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
      </section>
    </main>

    <div v-if="sessionDialogOpen" class="session-dialog-backdrop" @click.self="closeSessionOverlay">
      <section class="session-dialog" role="dialog" aria-modal="true">
        <header class="session-dialog-header">
          <h2>{{ sessionDialogTitle }}</h2>
          <div class="session-dialog-actions">
            <button class="btn btn-outline-secondary icon-button" type="button" title="Close" @click="closeSessionOverlay">
              <i class="bi bi-x-lg"></i>
            </button>
          </div>
        </header>
        <AgentViewer v-if="sessionDialogRef" :agent-ref="sessionDialogRef" pane-id="agent-task-session-dialog" />
      </section>
    </div>
  </section>
</template>

<style scoped>
.agent-task-page {
  display: grid;
  grid-template-columns: 44px 1fr;
  height: 100%;
  min-height: 0;
  background: #f7f8fb;
}

.task-rail {
  background: #ffffff;
  border-right: 1px solid #d8dee8;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 8px 5px;
}

.task-rail-button {
  width: 32px;
  height: 32px;
  border: 1px solid #d8dee8;
  border-radius: 6px;
  background: #ffffff;
  color: #44526a;
}

.task-rail-button.active {
  background: #edf4ff;
  border-color: #2f6fed;
  color: #174ea6;
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
.task-group-row,
.task-mode-row,
.task-detail-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.task-group-row {
  align-items: end;
}

.group-select-label {
  flex: 1;
  min-width: 0;
}

.task-panel-header,
.task-detail-header,
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
.task-node-meta {
  color: #667085;
  font-size: 12px;
}

.task-mode-row .btn {
  flex: 1;
  min-width: 0;
}

.group-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 180px;
  overflow: auto;
  border-top: 1px solid #edf0f5;
  padding-top: 8px;
}

.group-list-item {
  display: grid;
  gap: 2px;
  width: 100%;
  border: 1px solid #d8dee8;
  border-radius: 6px;
  background: #ffffff;
  padding: 7px;
  text-align: left;
}

.group-list-item.active {
  border-color: #2f6fed;
  box-shadow: 0 0 0 1px #2f6fed inset;
}

.group-title {
  font-size: 13px;
  font-weight: 650;
}

.group-meta,
.group-goal {
  color: #667085;
  font-size: 11px;
  line-height: 1.25;
}

.group-goal {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.manager-box {
  display: flex;
  flex-direction: column;
  gap: 8px;
  border-top: 1px solid #edf0f5;
  padding-top: 10px;
}

.manager-box .task-section-header span {
  color: #667085;
  font-size: 11px;
}

.task-tree {
  min-height: 0;
  overflow: auto;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.task-tree-node {
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

.task-tree-node.active {
  border-color: #2f6fed;
  box-shadow: 0 0 0 1px #2f6fed inset;
}

.task-main {
  min-width: 0;
  min-height: 0;
  overflow: auto;
  padding: 12px;
}

.task-panel {
  min-width: 0;
}

.task-dag-shell {
  background: #ffffff;
  border: 1px solid #d8dee8;
  border-radius: 8px;
  overflow: hidden;
}

.task-status-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 10px;
  padding: 9px 12px;
  border-bottom: 1px solid #edf0f5;
  background: #fbfcfe;
}

.task-legend-item {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: #667085;
  font-size: 11px;
}

.task-legend-dot {
  width: 8px;
  height: 8px;
  border-radius: 999px;
}

.task-dag-scroll {
  min-height: 260px;
  max-height: 58vh;
  overflow: auto;
  background:
    linear-gradient(#eef2f7 1px, transparent 1px),
    linear-gradient(90deg, #eef2f7 1px, transparent 1px),
    #f8fafc;
  background-size: 28px 28px;
}

.task-dag-canvas {
  position: relative;
  min-width: 100%;
  min-height: 260px;
}

.task-dag-edges {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  pointer-events: none;
}

.task-dag-edge {
  fill: none;
  stroke: #9aa7bb;
  stroke-linecap: round;
  stroke-width: 2;
  opacity: 0.7;
}

.task-dag-edge.active {
  stroke: #2563eb;
  stroke-width: 5;
  opacity: 0.95;
}

.task-dag-edge.dimmed {
  opacity: 0.18;
}

.task-dag-node {
  position: absolute;
  display: flex;
  flex-direction: column;
  gap: 6px;
  justify-content: center;
  border: 1px solid #d6dde8;
  border-left-width: 5px;
  border-radius: 8px;
  background: #ffffff;
  color: #172033;
  box-shadow: 0 8px 20px rgb(15 23 42 / 0.08);
  padding: 10px 12px;
  text-align: left;
  transition: border-color 0.12s ease, box-shadow 0.12s ease, opacity 0.12s ease, transform 0.12s ease;
}

.task-dag-node:hover,
.task-dag-node.active {
  border-color: #2563eb;
  box-shadow: 0 12px 28px rgb(37 99 235 / 0.18);
  transform: translateY(-1px);
}

.task-dag-node.active {
  outline: 2px solid rgb(37 99 235 / 0.22);
  outline-offset: 2px;
}

.task-dag-node.dimmed {
  opacity: 0.42;
}

.task-node-topline,
.task-node-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  color: #667085;
  font-size: 11px;
}

.task-node-status {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-node-title {
  display: -webkit-box;
  overflow: hidden;
  color: #172033;
  font-size: 13px;
  font-weight: 650;
  line-height: 1.25;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.task-dag-node.task-status-draft,
.task-dag-node.task-status-backlog {
  border-left-color: #94a3b8;
  background: #ffffff;
  color: #172033;
}

.task-dag-node.task-status-ready {
  border-left-color: #14b8a6;
  background: #f0fdfa;
  color: #134e4a;
}

.task-dag-node.task-status-running,
.task-dag-node.task-status-waiting-process {
  border-left-color: #3b82f6;
  background: #eff6ff;
  color: #1e3a8a;
}

.task-dag-node.task-status-review,
.task-dag-node.task-status-blocked {
  border-left-color: #f59e0b;
  background: #fffbeb;
  color: #78350f;
}

.task-dag-node.task-status-done {
  border-left-color: #22c55e;
  background: #f0fdf4;
  color: #14532d;
}

.task-dag-node.task-status-failed,
.task-dag-node.task-status-cancelled {
  border-left-color: #ef4444;
  background: #fef2f2;
  color: #7f1d1d;
}

.task-detail {
  margin-top: 12px;
  background: #ffffff;
  border: 1px solid #d8dee8;
  border-radius: 6px;
  padding: 14px;
}

.plan-editor {
  margin-bottom: 12px;
}

.plan-markdown-preview {
  margin-top: 12px;
  min-height: 160px;
  max-height: 55vh;
  overflow: auto;
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

.task-legend-dot.task-status-draft,
.task-legend-dot.task-status-backlog {
  background: #94a3b8;
}

.task-legend-dot.task-status-ready {
  background: #14b8a6;
}

.task-legend-dot.task-status-done {
  background: #22c55e;
}

.task-status-pill.task-status-ready,
.task-status-pill.task-status-done {
  background: #dcfce7;
  color: #166534;
}

.task-legend-dot.task-status-running,
.task-legend-dot.task-status-waiting-process {
  background: #3b82f6;
}

.task-status-pill.task-status-running,
.task-status-pill.task-status-waiting-process {
  background: #dbeafe;
  color: #1d4ed8;
}

.task-legend-dot.task-status-review,
.task-legend-dot.task-status-blocked {
  background: #f59e0b;
}

.task-status-pill.task-status-review,
.task-status-pill.task-status-blocked {
  background: #fef3c7;
  color: #92400e;
}

.task-legend-dot.task-status-failed,
.task-legend-dot.task-status-cancelled {
  background: #ef4444;
}

.task-status-pill.task-status-failed,
.task-status-pill.task-status-cancelled {
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

.session-dialog-backdrop {
  position: fixed;
  inset: 0;
  z-index: 80;
  background: rgb(15 23 42 / 0.32);
  display: grid;
  place-items: center;
  padding: 24px;
}

.session-dialog {
  width: min(1080px, 96vw);
  height: min(820px, 92vh);
  min-height: 0;
  background: #ffffff;
  border: 1px solid #cfd8e6;
  border-radius: 8px;
  box-shadow: 0 18px 48px rgb(15 23 42 / 0.24);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.session-dialog-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 10px 12px;
  border-bottom: 1px solid #d8dee8;
}

.session-dialog-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.session-dialog :deep(.agent-transcript-scroll) {
  flex: 1;
  min-height: 0;
}

.session-dialog :deep(.agent-viewer) {
  flex: 1;
  min-height: 0;
}

@media (max-width: 900px) {
  .agent-task-page {
    display: grid;
    grid-template-columns: 42px minmax(0, 1fr);
    height: auto;
    min-height: 100%;
    overflow: visible;
  }

  .task-rail {
    position: sticky;
    top: 0;
    align-self: start;
    min-height: calc(100vh - var(--topbar-height));
    z-index: 5;
  }

  .task-left {
    max-height: none;
    border-right: 0;
    border-bottom: 1px solid #d8dee8;
    position: static;
  }

  .task-panel-header {
    align-items: flex-start;
  }

  .task-group-row {
    display: grid;
    grid-template-columns: 1fr auto auto;
  }

  .group-list {
    display: grid;
    grid-auto-flow: column;
    grid-auto-columns: minmax(160px, 62vw);
    max-height: 92px;
    overflow-x: auto;
    overflow-y: hidden;
    padding-bottom: 4px;
  }

  .task-tree {
    display: none;
  }

  .task-main {
    padding: 10px;
    overflow: visible;
  }

  .task-dag-scroll {
    max-height: 62vh;
  }

  .task-status-legend {
    flex-wrap: nowrap;
    overflow-x: auto;
    white-space: nowrap;
  }

  .task-detail-header,
  .task-detail-actions {
    align-items: stretch;
    flex-direction: column;
  }

  .task-detail-actions .btn {
    width: 100%;
  }

  .task-form-grid,
  .task-subgrid {
    grid-template-columns: 1fr;
  }
}
</style>
