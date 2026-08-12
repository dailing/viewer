<script setup lang="ts">
/**
 * Terminal pane (framework appendix A.4): xterm.js view over a PTY runtime.
 *
 * View/runtime split: closing this pane never kills the PTY; reopening
 * reconnects via the explicit `snapshot` RPC (scrollback history from the
 * plugin's ring buffer, framework section 5.6) + live `output` events.
 * Input goes back as RPC (`request terminal:{id}:input`, framework section
 * 5.1 example); resize is a fire-and-forget publish.
 */

import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import { computed, inject, onBeforeUnmount, onMounted, ref } from "vue";

import "@xterm/xterm/css/xterm.css";

import type { PluginCtx } from "../../shell/ctx";

interface OutputEntry {
  seq: number;
  ts: number;
  data: string;
}

interface TerminalStatus {
  id: string;
  state: "running" | "exited" | "killed";
  exit_code: number | null;
  cols: number;
  rows: number;
}

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("TerminalPane must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const termId = ctx.instanceId;
const containerRef = ref<HTMLElement | null>(null);
const status = ref<TerminalStatus | null>(null);
const loadError = ref<string | null>(null);

const statusBanner = computed(() => {
  if (status.value === null || status.value.state === "running") return null;
  const code = status.value.exit_code;
  return status.value.state === "exited" ? `进程已退出 (exit ${code ?? "?"})` : "终端已终止";
});

let term: Terminal | null = null;
let fit: FitAddon | null = null;
let resizeObserver: ResizeObserver | null = null;

let snapshotDone = false;
let lastSeq = 0;
const pending: OutputEntry[] = [];

function applyEntry(entry: OutputEntry): void {
  if (entry.seq <= lastSeq || term === null) return;
  lastSeq = entry.seq;
  term.write(entry.data);
}

function publishResize(): void {
  if (term === null) return;
  if (status.value !== null && status.value.cols === term.cols && status.value.rows === term.rows) {
    return;
  }
  ctx.bus.publish(`terminal:${termId}:resize`, { cols: term.cols, rows: term.rows }).catch(() => {});
}

onMounted(() => {
  const container = containerRef.value;
  if (container === null) return;

  term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
    theme: { background: "#1a1b26" },
  });
  fit = new FitAddon();
  term.loadAddon(fit);
  term.open(container);
  fit.fit();

  // Keystrokes/paste -> PTY via RPC (framework 5.1 request/response example).
  term.onData((data) => {
    ctx.bus.request(`terminal:${termId}:input`, { data }).catch(() => {});
  });

  // Status mailbox: current value on subscribe + full replacements.
  ctx.bus.subscribe(`terminal:${termId}:status`, (frame) => {
    status.value = frame.value as TerminalStatus;
  });

  // Subscribe BEFORE the snapshot RPC so no output is lost in the gap;
  // buffer live entries until the snapshot is applied, then dedupe by seq.
  ctx.bus.subscribe(`terminal:${termId}:output`, (frame) => {
    const entry = frame.value as OutputEntry;
    if (!snapshotDone) pending.push(entry);
    else applyEntry(entry);
  });

  void (async () => {
    try {
      const snapshot = (await ctx.bus.request(`terminal:${termId}:snapshot`, {
        limit: 500,
      })) as { entries: OutputEntry[] };
      for (const entry of snapshot.entries) applyEntry(entry);
      loadError.value = null;
    } catch {
      loadError.value = "无法加载终端（不存在或 terminal 插件不在线）";
    } finally {
      snapshotDone = true;
      pending.sort((a, b) => a.seq - b.seq).forEach(applyEntry);
      pending.length = 0;
    }
  })();

  resizeObserver = new ResizeObserver(() => {
    if (fit === null) return;
    fit.fit();
    publishResize();
  });
  resizeObserver.observe(container);
  publishResize();
});

onBeforeUnmount(() => {
  resizeObserver?.disconnect();
  term?.dispose();
  term = null;
});
</script>

<template>
  <div class="d-flex flex-column h-100 terminal-pane">
    <div
      v-if="statusBanner !== null || loadError !== null"
      class="small px-3 py-1 border-bottom"
      :class="loadError !== null ? 'text-bg-warning' : 'text-bg-secondary'"
    >
      {{ loadError ?? statusBanner }}
    </div>
    <div ref="containerRef" class="flex-grow-1 overflow-hidden px-1" />
  </div>
</template>

<style scoped>
.terminal-pane {
  background: #1a1b26;
}
</style>
