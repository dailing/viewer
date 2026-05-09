<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { useAgentLoopsStore } from "../stores/agentLoops";
import { useCodexStore } from "../stores/codex";
import { renderMarkdown } from "../utils/markdownRender";
import type { AgentLoopDefinition, AgentLoopInfo, AgentLoopRunRecord } from "../types/agentLoops";
import type { CodexEvent } from "../types/codex";

const loops = useAgentLoopsStore();
const codex = useCodexStore();
const mode = ref<"edit" | "logs">("edit");
const saving = ref(false);
const busy = ref(false);
const error = ref("");
const expandedRunId = ref("");
const draft = reactive<AgentLoopDefinition>(emptyDefinition());

const selectedTask = computed(() => loops.tasks.find((task) => task.definition.id === loops.selectedId) ?? null);
const selectedRuns = computed(() => (loops.selectedId ? loops.runsByTask[loops.selectedId] ?? [] : []));
const selectedRunDetail = computed(() => {
  if (!loops.selectedId || !expandedRunId.value) return null;
  return loops.runDetails[`${loops.selectedId}:${expandedRunId.value}`] ?? null;
});

onMounted(async () => {
  await Promise.all([loops.load(), codex.load()]);
  syncDraft(selectedTask.value);
  if (loops.selectedId) await loops.loadRuns(loops.selectedId);
});

watch(selectedTask, (task) => syncDraft(task), { deep: true });

watch(
  () => loops.selectedId,
  async (id) => {
    expandedRunId.value = "";
    if (id) await loops.loadRuns(id);
  },
);

function emptyDefinition(): AgentLoopDefinition {
  return {
    id: "",
    name: "",
    enabled: true,
    agent: "codex",
    model: "gpt-5.5",
    cwd: "",
    timezone: "Asia/Shanghai",
    schedule: {
      type: "manual",
      at_local: null,
      start_at_local: null,
      every_minutes: 30,
      time_local: "09:00",
      times_local: [],
    },
    run: {
      max_runs: null,
      max_consecutive_failures: 3,
      skip_if_previous_running: true,
    },
    session: {
      policy: "reuse_until_context",
      max_context_percent: 70,
      reset_after_runs: null,
      reset_on_failure: false,
    },
    stop: {
      final_message_regex: "",
    },
    prompt: "",
  };
}

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function syncDraft(task: AgentLoopInfo | null) {
  Object.assign(draft, task ? clone(task.definition) : emptyDefinition());
  if (!draft.model) draft.model = codex.models.selected_model;
}

function localDatetimeValue(value?: string | null) {
  return value ? value.slice(0, 16) : "";
}

function setNullableNumber(target: Record<string, unknown>, key: string, value: string) {
  const number = Number(value);
  target[key] = value === "" || !Number.isFinite(number) ? null : Math.max(1, Math.round(number));
}

function setTimes(value: string) {
  draft.schedule.times_local = value
    .split(/\r?\n|,/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function formatTs(value?: number | null) {
  if (!value) return "Never";
  return new Intl.DateTimeFormat(undefined, {
    timeZone: "Asia/Shanghai",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value * 1000));
}

function statusClass(status?: string | null) {
  return {
    "status-running": status === "running",
    "status-failed": status === "failed",
    "status-exited": status === "exited",
  };
}

async function createTask() {
  const name = window.prompt("Task name", "New Loop Task");
  if (!name) return;
  await withBusy(async () => {
    await loops.create(name);
    mode.value = "edit";
  });
}

async function saveTask() {
  if (!draft.id) return;
  await withBusy(async () => {
    saving.value = true;
    try {
      await loops.save(clone(draft));
    } finally {
      saving.value = false;
    }
  });
}

async function deleteTask() {
  if (!draft.id || !window.confirm(`Delete loop task "${draft.name}"?`)) return;
  await withBusy(async () => loops.remove(draft.id));
}

async function runNow() {
  if (!draft.id) return;
  await withBusy(async () => {
    await loops.runNow(draft.id);
    await loops.loadRuns(draft.id);
  });
}

async function togglePaused() {
  const task = selectedTask.value;
  if (!task) return;
  await withBusy(async () => {
    if (task.runtime.paused) {
      await loops.resume(task.definition.id);
    } else {
      await loops.pause(task.definition.id);
    }
  });
}

async function resetSession() {
  if (!draft.id) return;
  await withBusy(async () => {
    await loops.resetSession(draft.id);
  });
}

async function reloadTasks() {
  await withBusy(async () => {
    await loops.reload();
    if (loops.selectedId) await loops.loadRuns(loops.selectedId);
  });
}

async function toggleRun(run: AgentLoopRunRecord) {
  if (!loops.selectedId) return;
  expandedRunId.value = expandedRunId.value === run.run_id ? "" : run.run_id;
  if (expandedRunId.value) await loops.loadRunDetail(loops.selectedId, run.run_id);
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

function rendered(text: string) {
  return renderMarkdown(text || "");
}

function visibleEvents(run: AgentLoopRunRecord | null): CodexEvent[] {
  return run?.session_snapshot?.events ?? [];
}
</script>

<template>
  <section class="loop-page">
    <aside class="loop-list">
      <div class="loop-list-header">
        <h2>Loop Tasks</h2>
        <div class="loop-list-actions">
          <button class="btn btn-outline-secondary icon-button" type="button" title="Reload task files" @click="reloadTasks">
            <i class="bi bi-arrow-clockwise"></i>
          </button>
          <button class="btn btn-primary icon-button" type="button" title="New task" @click="createTask">
            <i class="bi bi-plus-lg"></i>
          </button>
        </div>
      </div>
      <div class="loop-list-scroll">
        <button
          v-for="task in loops.tasks"
          :key="task.definition.id"
          class="loop-list-item"
          :class="{ active: task.definition.id === loops.selectedId }"
          type="button"
          @click="loops.selectedId = task.definition.id"
        >
          <span class="loop-list-name">{{ task.definition.name }}</span>
          <span class="loop-list-meta">
            <span class="loop-status-dot" :class="statusClass(task.runtime.last_status)"></span>
            {{ task.runtime.current_run_id ? "Running" : task.runtime.paused ? "Paused" : task.runtime.stopped ? "Stopped" : "Ready" }}
          </span>
          <span class="loop-list-next">Next {{ formatTs(task.runtime.next_run_at) }}</span>
        </button>
        <div v-if="!loops.tasks.length" class="loop-empty">No loop tasks yet.</div>
      </div>
    </aside>

    <main class="loop-detail">
      <header class="loop-detail-header">
        <div>
          <h1>{{ draft.name || "Loop Task" }}</h1>
          <span v-if="draft.id">~/.view/loops/{{ draft.id }}.md</span>
        </div>
        <div class="loop-tabs" role="tablist">
          <button class="btn btn-outline-secondary" :class="{ active: mode === 'edit' }" type="button" @click="mode = 'edit'">Edit</button>
          <button class="btn btn-outline-secondary" :class="{ active: mode === 'logs' }" type="button" @click="mode = 'logs'">Logs</button>
        </div>
      </header>

      <div v-if="error" class="alert alert-danger">{{ error }}</div>

      <div v-if="!draft.id" class="loop-empty-detail">
        <button class="btn btn-primary" type="button" @click="createTask">Create Loop Task</button>
      </div>

      <form v-else-if="mode === 'edit'" class="loop-editor" @submit.prevent="saveTask">
        <section class="loop-form-grid">
          <label>
            <span>Name</span>
            <input v-model="draft.name" class="form-control" type="text" />
          </label>
          <label>
            <span>Working Dir</span>
            <input v-model="draft.cwd" class="form-control" type="text" placeholder="served-root relative path" />
          </label>
          <label>
            <span>Model</span>
            <select v-model="draft.model" class="form-select">
              <option v-for="model in codex.models.available_models" :key="model" :value="model">{{ model }}</option>
            </select>
          </label>
          <label>
            <span>Timezone</span>
            <input class="form-control" type="text" value="UTC+8 / Asia/Shanghai" disabled />
          </label>
          <label class="loop-check">
            <input v-model="draft.enabled" class="form-check-input" type="checkbox" />
            <span>Enabled</span>
          </label>
          <label class="loop-check">
            <input v-model="draft.run.skip_if_previous_running" class="form-check-input" type="checkbox" />
            <span>Skip if previous run is active</span>
          </label>
        </section>

        <section class="loop-form-section">
          <h3>Schedule</h3>
          <div class="loop-form-grid">
            <label>
              <span>Type</span>
              <select v-model="draft.schedule.type" class="form-select">
                <option value="manual">Manual</option>
                <option value="once">Once</option>
                <option value="interval">Interval</option>
                <option value="daily">Daily</option>
                <option value="multi_daily">Multi Daily</option>
              </select>
            </label>
            <label v-if="draft.schedule.type === 'once'">
              <span>Run At</span>
              <input
                class="form-control"
                type="datetime-local"
                :value="localDatetimeValue(draft.schedule.at_local)"
                @input="draft.schedule.at_local = ($event.target as HTMLInputElement).value || null"
              />
            </label>
            <label v-if="draft.schedule.type === 'interval'">
              <span>Every Minutes</span>
              <input v-model.number="draft.schedule.every_minutes" class="form-control" type="number" min="1" />
            </label>
            <label v-if="draft.schedule.type === 'interval'">
              <span>Start At</span>
              <input
                class="form-control"
                type="datetime-local"
                :value="localDatetimeValue(draft.schedule.start_at_local)"
                @input="draft.schedule.start_at_local = ($event.target as HTMLInputElement).value || null"
              />
            </label>
            <label v-if="draft.schedule.type === 'daily'">
              <span>Time</span>
              <input v-model="draft.schedule.time_local" class="form-control" type="time" />
            </label>
            <label v-if="draft.schedule.type === 'multi_daily'" class="loop-wide">
              <span>Times</span>
              <textarea
                class="form-control"
                rows="3"
                :value="draft.schedule.times_local.join('\n')"
                @input="setTimes(($event.target as HTMLTextAreaElement).value)"
              ></textarea>
            </label>
          </div>
        </section>

        <section class="loop-form-section">
          <h3>Run Limits</h3>
          <div class="loop-form-grid">
            <label>
              <span>Max Runs</span>
              <input
                class="form-control"
                type="number"
                min="1"
                :value="draft.run.max_runs ?? ''"
                @input="setNullableNumber(draft.run, 'max_runs', ($event.target as HTMLInputElement).value)"
              />
            </label>
            <label>
              <span>Max Consecutive Failures</span>
              <input
                class="form-control"
                type="number"
                min="1"
                :value="draft.run.max_consecutive_failures ?? ''"
                @input="setNullableNumber(draft.run, 'max_consecutive_failures', ($event.target as HTMLInputElement).value)"
              />
            </label>
          </div>
        </section>

        <section class="loop-form-section">
          <h3>Session</h3>
          <div class="loop-form-grid">
            <label>
              <span>Policy</span>
              <select v-model="draft.session.policy" class="form-select">
                <option value="new_each_run">New each run</option>
                <option value="reuse">Reuse</option>
                <option value="reuse_until_context">Reuse until context</option>
                <option value="reuse_with_limits">Reuse with limits</option>
              </select>
            </label>
            <label>
              <span>Max Context %</span>
              <input v-model.number="draft.session.max_context_percent" class="form-control" type="number" min="1" max="100" />
            </label>
            <label>
              <span>Reset After Runs</span>
              <input
                class="form-control"
                type="number"
                min="1"
                :value="draft.session.reset_after_runs ?? ''"
                @input="setNullableNumber(draft.session, 'reset_after_runs', ($event.target as HTMLInputElement).value)"
              />
            </label>
            <label class="loop-check">
              <input v-model="draft.session.reset_on_failure" class="form-check-input" type="checkbox" />
              <span>Reset on failure</span>
            </label>
          </div>
        </section>

        <section class="loop-form-section">
          <h3>Stop</h3>
          <label>
            <span>Final Message Regex</span>
            <input v-model="draft.stop.final_message_regex" class="form-control" type="text" />
          </label>
        </section>

        <section class="loop-form-section loop-prompt-section">
          <h3>Prompt</h3>
          <textarea v-model="draft.prompt" class="form-control loop-prompt" spellcheck="false"></textarea>
        </section>

        <footer class="loop-editor-actions">
          <button class="btn btn-primary" type="submit" :disabled="saving || busy">Save</button>
          <button class="btn btn-outline-secondary" type="button" :disabled="busy" @click="runNow">Run Now</button>
          <button class="btn btn-outline-secondary" type="button" :disabled="busy" @click="togglePaused">
            {{ selectedTask?.runtime.paused ? "Resume" : "Pause" }}
          </button>
          <button class="btn btn-outline-secondary" type="button" :disabled="busy" @click="resetSession">Reset Session</button>
          <button class="btn btn-outline-danger ms-auto" type="button" :disabled="busy" @click="deleteTask">Delete</button>
        </footer>
      </form>

      <section v-else class="loop-logs">
        <div class="loop-log-toolbar">
          <button class="btn btn-outline-secondary" type="button" @click="loops.loadRuns(draft.id)">Refresh Logs</button>
        </div>
        <article v-for="run in selectedRuns" :key="run.run_id" class="loop-run">
          <button class="loop-run-summary" type="button" @click="toggleRun(run)">
            <span>{{ formatTs(run.started_at) }}</span>
            <span :class="statusClass(run.status)">{{ run.status }}</span>
            <span>{{ run.codex_session_id || "no session" }}</span>
          </button>
          <div v-if="expandedRunId === run.run_id" class="loop-run-detail">
            <div v-if="!selectedRunDetail" class="loop-loading">Loading...</div>
            <template v-else>
              <div class="loop-run-meta">
                <span>Started {{ formatTs(selectedRunDetail.started_at) }}</span>
                <span>Finished {{ formatTs(selectedRunDetail.finished_at) }}</span>
                <span>Exit {{ selectedRunDetail.exit_code ?? "n/a" }}</span>
              </div>
              <div v-if="selectedRunDetail.error" class="alert alert-danger">{{ selectedRunDetail.error }}</div>
              <div v-if="selectedRunDetail.prompt" class="codex-log-entry prompt">
                <strong>Prompt</strong>
                <div v-html="rendered(selectedRunDetail.prompt)"></div>
              </div>
              <div v-for="event in visibleEvents(selectedRunDetail)" :key="event.index" class="codex-log-entry">
                <span class="codex-log-type">{{ event.event_type }}</span>
                <div v-if="event.text" v-html="rendered(event.text)"></div>
                <pre v-if="event.patch_text">{{ event.patch_text }}</pre>
                <pre v-for="change in event.file_changes" :key="`${event.index}:${change.path}`">{{ change.path }}
{{ change.diff }}</pre>
              </div>
            </template>
          </div>
        </article>
        <div v-if="!selectedRuns.length" class="loop-empty">No runs yet.</div>
      </section>
    </main>
  </section>
</template>
