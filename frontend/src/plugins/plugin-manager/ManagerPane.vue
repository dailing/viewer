<script setup lang="ts">
/**
 * Unified plugin manager (framework v1.00): built-ins are listed as enabled;
 * external entries retain launch path/command/autostart and process lifecycle
 * controls. Plugin-owned settings mount below runtime status through the same
 * registry for both module kinds. Layout follows section 8.9 master-detail.
 */
import { computed, inject, onMounted, ref } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { pluginCatalog, settingsRegistry } from "../../shell/registries";
import { errorText } from "../chat/types";
import MasterDetail from "../../shell/MasterDetail.vue";
import PluginSettingsHost from "./PluginSettingsHost.vue";

interface PluginEntry {
  id: string;
  name?: string;
  path: string;
  command?: string[];
  enabled: boolean;
  autostart: boolean;
  state?: string;
  pid?: number;
  exit_code?: number;
  crashes?: number;
  /** In-repo plugin: listed as enabled, without external process controls. */
  builtin?: boolean;
}

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("ManagerPane requires PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const entries = ref<PluginEntry[]>([]);
const selectedID = ref("");
const error = ref("");
const busy = ref(false);
/** Draft for a not-yet-registered plugin. */
const draft = ref<PluginEntry | null>(null);
/** Command line edited as one text field, split on whitespace into argv. */
const commandText = ref("");

const STATE_LABELS: Record<string, string> = {
  starting: "启动中",
  running: "运行中",
  crashed: "已崩溃（等待重试）",
  broken: "失败（重试已用尽）",
  stopped: "已停止",
};

const builtinEntries = computed<PluginEntry[]>(() => [...pluginCatalog.values()]
  .filter((plugin) => plugin.builtin)
  .map((plugin) => ({
    id: plugin.id,
    name: plugin.name,
    path: "",
    enabled: true,
    autostart: true,
    state: "running",
    builtin: true,
  })));
const allEntries = computed(() => [
  ...builtinEntries.value,
  ...entries.value.filter((entry) => pluginCatalog.get(entry.id)?.builtin !== true),
]);
const selected = computed(() => draft.value ?? allEntries.value.find((item) => item.id === selectedID.value) ?? null);
const items = computed(() => allEntries.value.map((item) => ({ id: item.id, name: item.name || item.id })));
const selectedSettings = computed(() => selected.value === null || draft.value !== null
  ? undefined
  : settingsRegistry.get(selected.value.id));

function stateLabel(entry: PluginEntry): string {
  return STATE_LABELS[entry.state ?? ""] ?? entry.state ?? "—";
}

function stateClass(entry: PluginEntry): string {
  switch (entry.state) {
    case "running": return "text-success";
    case "starting": return "text-primary";
    case "crashed": case "broken": return "text-danger";
    default: return "text-secondary";
  }
}

function mergeStates(states: Record<string, { state: string; pid?: number; exit_code?: number; crashes?: number }>): void {
  let unknown = false;
  for (const entry of entries.value) {
    const live = states[entry.id];
    if (live) Object.assign(entry, live);
  }
  for (const id of Object.keys(states)) {
    if (!entries.value.some((entry) => entry.id === id)) unknown = true;
  }
  if (unknown) void load();
}

async function load(): Promise<void> {
  const list = await ctx.bus.request("supervisor:_:list", {}) as { plugins: PluginEntry[] };
  entries.value = list.plugins ?? [];
  if (selectedID.value && !allEntries.value.some((item) => item.id === selectedID.value)) selectedID.value = "";
  if (!selectedID.value && allEntries.value.length > 0) select(allEntries.value[0].id);
}

function select(id: string): void {
  draft.value = null;
  selectedID.value = id;
  const entry = allEntries.value.find((item) => item.id === id);
  commandText.value = (entry?.command ?? []).join(" ");
}

function createDraft(): void {
  selectedID.value = "";
  commandText.value = "";
  draft.value = { id: "", name: "", path: "", enabled: true, autostart: false, state: "stopped" };
}

async function save(): Promise<void> {
  const entry = selected.value;
  if (entry === null) return;
  error.value = "";
  busy.value = true;
  try {
    const command = commandText.value.trim().split(/\s+/).filter((part) => part !== "");
    await ctx.bus.request("supervisor:_:upsert", {
      id: entry.id.trim(),
      name: entry.name?.trim() ?? "",
      path: entry.path.trim(),
      command,
      enabled: entry.enabled,
      autostart: entry.autostart,
    });
    draft.value = null;
    selectedID.value = entry.id.trim();
    await load();
  } catch (cause) {
    error.value = errorText(cause);
  } finally {
    busy.value = false;
  }
}

async function action(rpc: string, id: string): Promise<void> {
  error.value = "";
  busy.value = true;
  try {
    await ctx.bus.request(`supervisor:_:${rpc}`, { id });
    if (rpc === "delete") selectedID.value = "";
    await load();
  } catch (cause) {
    error.value = errorText(cause);
  } finally {
    busy.value = false;
  }
}

async function remove(entry: PluginEntry): Promise<void> {
  if (!confirm(`删除插件 ${entry.name || entry.id}？其进程会被停止，前端资源会被移除。`)) return;
  await action("delete", entry.id);
}

onMounted(() => {
  ctx.bus.subscribe("supervisor:_:states", (frame) => {
    mergeStates((frame.value ?? {}) as Record<string, { state: string; pid?: number; exit_code?: number; crashes?: number }>);
  });
  void load().catch((cause: unknown) => { error.value = errorText(cause); });
});
</script>

<template>
  <MasterDetail :items="items" :selected-id="draft !== null ? '' : selectedID" create-label="＋ 新建插件" @select="select" @create="createDraft">
    <template #detail>
      <div v-if="error" class="small text-danger mb-2">{{ error }}</div>
      <div v-if="selected" class="plugin-config">
        <div v-if="!selected.builtin" class="row g-2">
          <label class="col-md-6 form-label small">ID<input v-model="selected.id" :disabled="draft === null" class="form-control form-control-sm mt-1 font-monospace" placeholder="my-plugin"></label>
          <label class="col-md-6 form-label small">名称<input v-model="selected.name" class="form-control form-control-sm mt-1" placeholder="展示名（可选）"></label>
          <label class="col-12 form-label small">插件目录<input v-model="selected.path" class="form-control form-control-sm mt-1 font-monospace" placeholder="/path/to/my-plugin"></label>
          <label class="col-12 form-label small">
            启动命令（留空 = 默认 backend/run；自动追加 --kernel-ws）
            <input v-model="commandText" class="form-control form-control-sm mt-1 font-monospace" placeholder="backend/run 或 /usr/bin/python3 backend/main.py">
          </label>
          <div class="col-12 d-flex gap-3">
            <label class="small"><input v-model="selected.enabled" type="checkbox" class="me-1">启用</label>
            <label class="small" title="Viewer 启动时自动拉起；默认手动启动"><input v-model="selected.autostart" type="checkbox" class="me-1">自动启动</label>
          </div>
        </div>
        <div v-else class="d-flex align-items-center gap-2">
          <strong>{{ selected.name || selected.id }}</strong>
          <code>{{ selected.id }}</code>
          <span class="badge text-bg-secondary">内置 · 默认启用</span>
        </div>

        <div v-if="draft === null" class="small mt-3">
          <span :class="stateClass(selected)">● {{ stateLabel(selected) }}</span>
          <span v-if="selected.pid" class="text-secondary ms-2">PID {{ selected.pid }}</span>
          <span v-if="selected.exit_code !== undefined" class="text-secondary ms-2">exit {{ selected.exit_code }}</span>
          <span v-if="(selected.crashes ?? 0) > 0" class="text-secondary ms-2">连续失败 {{ selected.crashes }} 次</span>
        </div>

        <div v-if="!selected.builtin" class="d-flex align-items-center gap-1 mt-3">
          <button class="btn btn-sm btn-primary" :disabled="busy" @click="save">保存</button>
          <template v-if="draft === null">
            <button v-if="selected.state !== 'running' && selected.state !== 'starting'" class="btn btn-sm btn-outline-success" :disabled="busy" @click="action('start', selected.id)"><i class="bi bi-play-fill me-1"></i>启动</button>
            <button v-else class="btn btn-sm btn-outline-warning" :disabled="busy" @click="action('stop', selected.id)"><i class="bi bi-stop-fill me-1"></i>停止</button>
            <button class="btn btn-sm btn-outline-secondary" :disabled="busy" title="停止并重新启动" @click="action('restart', selected.id)"><i class="bi bi-arrow-clockwise"></i></button>
            <button class="btn btn-sm btn-outline-danger ms-auto" :disabled="busy" @click="remove(selected)"><i class="bi bi-trash me-1"></i>删除</button>
          </template>
        </div>
        <section v-if="selectedSettings && draft === null" class="border-top mt-3 pt-3">
          <div class="fw-semibold small mb-2">插件设置</div>
          <PluginSettingsHost :key="selected.id" :plugin-id="selected.id" :component="selectedSettings" />
        </section>
        <div v-if="!selected.builtin" class="small text-secondary mt-3">
          外部插件的后端进程也可以不由这里管理：直接运行 <code>backend/run --kernel-ws ws://127.0.0.1:8765/ws</code> 即可 attach（standalone 模式）。前端 bundle 由插件后端在 hello 后通过 <code>gateway:_:assets:push</code> 推送，shell 收到后自动加载，无需刷新页面。
        </div>
      </div>
      <div v-else-if="!error" class="small text-secondary">选择插件以查看状态与配置，或新建一个外部插件条目。</div>
    </template>
  </MasterDetail>
</template>
