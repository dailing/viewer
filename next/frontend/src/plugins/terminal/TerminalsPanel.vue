<script setup lang="ts">
/**
 * Terminals panel (sidebar tool): list live terminals + spawn new ones.
 *
 * Terminal metadata is runtime state: the list comes from the explicit
 * `terminal:_:list` RPC, live updates from `terminal:*:status` mailboxes
 * (full current value per terminal — subscribe replays retained values).
 */

import { computed, inject, onMounted, reactive, ref } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { useShellStore } from "../../stores/shell";

interface TerminalStatus {
  id: string;
  state: "running" | "exited" | "killed";
  exit_code: number | null;
  pid: number;
  cwd: string;
  shell: string;
  cols: number;
  rows: number;
  created_ts: number;
}

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("TerminalsPanel must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const shell = useShellStore();

const terminals = reactive(new Map<string, TerminalStatus>());
const error = ref<string | null>(null);
const creating = ref(false);

const sortedTerminals = computed(() =>
  [...terminals.values()].sort((a, b) => Number(a.id) - Number(b.id)),
);

onMounted(() => {
  ctx.bus.subscribe("terminal:*:status", (frame) => {
    const value = frame.value as TerminalStatus;
    if (value && typeof value.id === "string") terminals.set(value.id, value);
  });
  void refresh();
});

async function refresh(): Promise<void> {
  try {
    const list = (await ctx.bus.request("terminal:_:list")) as TerminalStatus[];
    terminals.clear();
    for (const entry of list) terminals.set(entry.id, entry);
    error.value = null;
  } catch {
    error.value = "terminal 插件不在线";
  }
}

async function createTerminal(): Promise<void> {
  creating.value = true;
  try {
    const result = (await ctx.bus.request("terminal:_:create", {})) as { id: string };
    shell.openPane("terminal", result.id);
    error.value = null;
  } catch {
    error.value = "创建终端失败（terminal 插件不在线？）";
  } finally {
    creating.value = false;
  }
}

async function killTerminal(id: string): Promise<void> {
  try {
    await ctx.bus.request(`terminal:${id}:kill`);
  } catch {
    /* terminal already gone — the status mailbox will settle */
  }
}

function stateBadge(state: string): string {
  if (state === "running") return "text-bg-success";
  if (state === "exited") return "text-bg-secondary";
  return "text-bg-danger";
}
</script>

<template>
  <div class="d-flex flex-column h-100">
    <div class="d-flex align-items-center border-bottom px-3 py-2 gap-2">
      <strong class="small">终端</strong>
      <span class="text-muted small">{{ terminals.size }} 个</span>
      <button
        type="button"
        class="btn btn-sm btn-outline-primary ms-auto"
        :disabled="creating"
        title="新建终端"
        @click="createTerminal"
      >
        <i class="bi" :class="creating ? 'bi-hourglass-split' : 'bi-plus-lg'"></i>
      </button>
      <button type="button" class="btn btn-sm btn-outline-secondary" title="刷新列表" @click="refresh">
        <i class="bi bi-arrow-clockwise"></i>
      </button>
    </div>
    <div v-if="error !== null" class="alert alert-warning small m-2 py-1 px-2 mb-0">{{ error }}</div>
    <div class="flex-grow-1 overflow-auto p-2">
      <div v-if="sortedTerminals.length === 0" class="text-muted small text-center mt-4">
        没有终端 — 点右上角 <i class="bi bi-plus-lg"></i> 新建
      </div>
      <div
        v-for="term in sortedTerminals"
        :key="term.id"
        class="d-flex align-items-center border rounded px-2 py-1 mb-1 term-row"
        role="button"
        @click="shell.openPane('terminal', term.id)"
      >
        <span class="badge me-2" :class="stateBadge(term.state)">{{ term.state }}</span>
        <span class="small font-monospace">#{{ term.id }}</span>
        <span class="text-muted small ms-2 text-truncate">{{ term.shell }} · {{ term.cwd }}</span>
        <span class="text-muted small ms-auto me-2">{{ term.cols }}×{{ term.rows }}</span>
        <button
          type="button"
          class="btn btn-sm btn-outline-danger border-0"
          :disabled="term.state !== 'running'"
          title="终止终端"
          @click.stop="killTerminal(term.id)"
        >
          <i class="bi bi-x-lg"></i>
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.term-row {
  cursor: pointer;
}
.term-row:hover {
  background: var(--bs-tertiary-bg);
}
</style>
