<script setup lang="ts">
/**
 * Bus Inspector pane (framework A.10): live bus traffic debugger.
 *
 * Data flow per framework section 5.6: history comes from the explicit
 * paginated `snapshot` RPC; the live tail comes from the
 * `bus-inspector:_:matches` event stream; counters from the
 * `bus-inspector:_:stats` mailbox. Display never auto-scrolls — scroll is
 * manual only.
 */

import { computed, inject, onMounted, ref } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { busState } from "../../shell/bus";

interface InspectorEntry {
  seq: number;
  ts: number;
  type: string;
  channel: string;
  origin: { plugin: string; instance: string };
  trace_id?: string;
  depth: number;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  value: any;
}

interface InspectorStats {
  captured: number;
  emitted: number;
  dropped: number;
  rate_per_sec: number;
  paused: boolean;
  filter: Record<string, string>;
  ring_size: number;
  ring_used: number;
}

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("InspectorPane must be mounted inside PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const DISPLAY_CAP = 1000;

const entries = ref<InspectorEntry[]>([]);
const stats = ref<InspectorStats | null>(null);
const snapshotAvailable = ref(true);
const filterChannel = ref(ctx.storage.get("filterChannel", ""));
const filterType = ref(ctx.storage.get("filterType", ""));
const filterOrigin = ref(ctx.storage.get("filterOrigin", ""));
const filterTrace = ref(ctx.storage.get("filterTrace", ""));
const filterText = ref(ctx.storage.get("filterText", ""));
const expandedSeq = ref<number | null>(null);

const paused = computed(() => stats.value?.paused ?? false);

function mergeEntries(incoming: InspectorEntry[]): void {
  const seen = new Set(entries.value.map((entry) => entry.seq));
  for (const entry of incoming) {
    if (!seen.has(entry.seq)) entries.value.push(entry);
  }
  entries.value.sort((a, b) => a.seq - b.seq);
  if (entries.value.length > DISPLAY_CAP) {
    entries.value.splice(0, entries.value.length - DISPLAY_CAP);
  }
}

async function loadSnapshot(beforeSeq?: number): Promise<void> {
  try {
    const result = await ctx.bus.request("bus-inspector:_:snapshot", {
      limit: 200,
      ...(beforeSeq !== undefined ? { before_seq: beforeSeq } : {}),
    });
    snapshotAvailable.value = true;
    mergeEntries((result?.entries ?? []) as InspectorEntry[]);
  } catch {
    // Inspector plugin not running: live subscriptions stay dark too; the pane
    // shows the empty state rather than failing (framework section 8.6 rule 5).
    snapshotAvailable.value = false;
  }
}

async function togglePause(): Promise<void> {
  const slot = paused.value ? "bus-inspector:_:resume" : "bus-inspector:_:pause";
  await ctx.bus.request(slot).catch(() => undefined);
}

async function clearAll(): Promise<void> {
  entries.value = [];
  await ctx.bus.request("bus-inspector:_:clear").catch(() => undefined);
}

async function applyFilter(): Promise<void> {
  ctx.storage.set("filterChannel", filterChannel.value);
  ctx.storage.set("filterType", filterType.value);
  ctx.storage.set("filterOrigin", filterOrigin.value);
  ctx.storage.set("filterTrace", filterTrace.value);
  ctx.storage.set("filterText", filterText.value);
  const filter: Record<string, string> = {};
  if (filterChannel.value.trim() !== "") filter.channel = filterChannel.value.trim();
  if (filterType.value !== "") filter.type = filterType.value;
  if (filterOrigin.value.trim() !== "") filter.origin = filterOrigin.value.trim();
  if (filterTrace.value.trim() !== "") filter.trace_id = filterTrace.value.trim();
  if (filterText.value.trim() !== "") filter.text = filterText.value.trim();
  await ctx.bus.request("bus-inspector:_:set-filter", filter).catch(() => undefined);
}

function formatTs(ts: number): string {
  // Kernel stamps ts in milliseconds.
  const date = new Date(ts);
  const hh = String(date.getHours()).padStart(2, "0");
  const mm = String(date.getMinutes()).padStart(2, "0");
  const ss = String(date.getSeconds()).padStart(2, "0");
  const ms = String(date.getMilliseconds()).padStart(3, "0");
  return `${hh}:${mm}:${ss}.${ms}`;
}

function renderValue(value: unknown, full: boolean): string {
  const text = JSON.stringify(value);
  if (full || text.length <= 300) return text;
  return `${text.slice(0, 300)}…`;
}

function typeBadge(type: string): string {
  switch (type) {
    case "set":
      return "text-bg-primary";
    case "publish":
      return "text-bg-secondary";
    default:
      return "text-bg-light text-dark";
  }
}

onMounted(() => {
  ctx.bus.subscribe("bus-inspector:_:matches", (frame) => {
    mergeEntries([frame.value as InspectorEntry]);
  });
  ctx.bus.subscribe("bus-inspector:_:stats", (frame) => {
    stats.value = frame.value as InspectorStats;
  });
  if (
    filterChannel.value !== "" ||
    filterType.value !== "" ||
    filterOrigin.value !== "" ||
    filterTrace.value !== "" ||
    filterText.value !== ""
  ) {
    void applyFilter();
  }
  void loadSnapshot();
});
</script>

<template>
  <div class="d-flex flex-column h-100">
    <div class="d-flex align-items-center gap-2 border-bottom px-3 py-2 flex-wrap">
      <strong>Bus Inspector</strong>
      <span v-if="stats !== null" class="text-muted small">
        捕获 {{ stats.captured }} · 丢弃 {{ stats.dropped }} · {{ stats.rate_per_sec.toFixed(0) }}/s
        · ring {{ stats.ring_used }}/{{ stats.ring_size }}
      </span>
      <span v-else-if="!snapshotAvailable" class="text-muted small">inspector 插件未运行</span>
      <div class="ms-auto d-flex align-items-center gap-1">
        <button
          type="button"
          class="btn btn-sm btn-outline-secondary"
          :class="{ active: paused }"
          :aria-pressed="paused"
          :title="paused ? '继续捕获' : '暂停捕获'"
          @click="togglePause"
        >
          <i class="bi" :class="paused ? 'bi-play-fill' : 'bi-pause-fill'"></i>
        </button>
        <button
          type="button"
          class="btn btn-sm btn-outline-secondary"
          title="清空 ring buffer 与显示"
          @click="clearAll"
        >
          <i class="bi bi-trash"></i>
        </button>
        <button
          type="button"
          class="btn btn-sm btn-outline-secondary"
          title="重新拉取快照"
          @click="loadSnapshot()"
        >
          <i class="bi bi-arrow-clockwise"></i>
        </button>
      </div>
    </div>

    <div class="d-flex align-items-center gap-2 border-bottom px-3 py-1 flex-wrap">
      <input
        v-model="filterChannel"
        type="text"
        class="form-control form-control-sm"
        style="max-width: 16rem"
        placeholder="channel pattern（如 chat:*:status）"
        @keyup.enter="applyFilter"
      />
      <select
        v-model="filterType"
        class="form-select form-select-sm"
        style="max-width: 8rem"
        aria-label="帧类型"
        @change="applyFilter"
      >
        <option value="">全部类型</option>
        <option value="publish">publish</option>
        <option value="set">set</option>
      </select>
      <input
        v-model="filterOrigin"
        type="text"
        class="form-control form-control-sm"
        style="max-width: 12rem"
        placeholder="origin plugin"
        @keyup.enter="applyFilter"
      />
      <input
        v-model="filterTrace"
        type="text"
        class="form-control form-control-sm"
        style="max-width: 12rem"
        placeholder="trace_id"
        @keyup.enter="applyFilter"
      />
      <input
        v-model="filterText"
        type="text"
        class="form-control form-control-sm"
        style="max-width: 16rem"
        placeholder="payload 包含文本"
        @keyup.enter="applyFilter"
      />
      <button type="button" class="btn btn-sm btn-outline-primary" title="应用过滤" @click="applyFilter">
        <i class="bi bi-funnel-fill"></i>
      </button>
      <span v-if="stats !== null && Object.keys(stats.filter).length > 0" class="badge text-bg-info">
        过滤中
      </span>
    </div>

    <div class="flex-grow-1 overflow-auto px-2 py-1 font-monospace small">
      <div v-if="entries.length === 0" class="text-muted text-center mt-4">
        {{ busState.connected ? "暂无捕获消息" : "等待总线连接…" }}
      </div>
      <div
        v-for="entry in entries"
        :key="entry.seq"
        class="border-bottom py-1 px-1 inspector-row"
        role="button"
        @click="expandedSeq = expandedSeq === entry.seq ? null : entry.seq"
      >
        <span class="text-muted">{{ formatTs(entry.ts) }}</span>
        <span class="badge ms-1" :class="typeBadge(entry.type)">{{ entry.type }}</span>
        <span class="ms-1 fw-bold">{{ entry.channel }}</span>
        <span class="ms-1 text-muted">{{ entry.origin.plugin }}:{{ entry.origin.instance }}</span>
        <span v-if="entry.trace_id" class="ms-1 text-muted" :title="`trace ${entry.trace_id}`">
          <i class="bi bi-link-45deg"></i>
        </span>
        <span class="ms-1 text-muted">d{{ entry.depth }}</span>
        <div class="text-body-secondary text-break">
          {{ renderValue(entry.value, expandedSeq === entry.seq) }}
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.inspector-row {
  cursor: pointer;
}
.inspector-row:hover {
  background-color: var(--bs-secondary-bg);
}
</style>
