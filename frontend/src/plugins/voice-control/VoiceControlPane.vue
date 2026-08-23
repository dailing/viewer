<script setup lang="ts">
/**
 * 语音控制 debug pane (framework §8.11): three tabs —
 *
 * - 日志: the backend interaction log (retained mailbox voice-control:_:log,
 *   live-updating): every exchange with transcript, raw LLM output, chosen
 *   catalog action, effects, timings, plus state-transition events.
 * - Prompt: the prompt assembly. System/summary templates are editable
 *   (config-store plugins.voice-control, re-read per call so saving takes
 *   effect immediately); a preview renders the exact message list a command
 *   would send right now.
 * - 功能目录: the merged voice catalog — which plugins were sniffed and what
 *   each of their entries does.
 */
import { computed, inject, onMounted, ref } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { getController } from "./controller";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("VoiceControlPane requires PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

interface VoiceEffect {
  type: string;
  plugin?: string;
  pane_type?: string;
  instance_id?: string;
}

interface LogEntry {
  ts: number;
  kind: string; // "exchange" | "event"
  phase?: string;
  transcript?: string;
  say?: string;
  llm_raw?: string;
  ok?: boolean;
  entry_id?: string;
  entry_title?: string;
  channel?: string;
  effects?: VoiceEffect[];
  detail?: string;
  llm_ms?: number;
  invoke_ms?: number;
}

interface CatalogEntry {
  id: string;
  plugin: string;
  kind: string;
  title: string;
  keywords?: string[];
  description?: string;
  pane_type?: string;
  instance_id?: string;
  channel: string;
}

interface PromptView {
  system_template: string;
  summary_template: string;
  defaults: { system_template: string; summary_template: string };
  summary: string;
  history: { Role?: string; role?: string; Content?: string; content?: string }[];
  messages: { role: string; content: string }[];
}

type Tab = "log" | "prompt" | "catalog";
const tab = ref<Tab>("log");

// ── Header: live state-machine phase from the shared controller ────────
const controller = getController();
const phase = computed(() => controller?.phase.value ?? "off");
const phaseLabel = computed(() => {
  switch (phase.value) {
    case "command": return "正在听";
    case "busy": return "处理中";
    case "awaiting": return "等待口述";
    default: return "已关闭";
  }
});

// ── 日志 tab ───────────────────────────────────────────────────────────
const logEntries = ref<LogEntry[]>([]);
ctx.bus.subscribe("voice-control:_:log", (frame) => {
  const value = frame.value as { entries?: LogEntry[] } | null;
  logEntries.value = value?.entries ?? [];
});
const logReversed = computed(() => [...logEntries.value].reverse());

function fmtTime(ts: number): string {
  return new Date(ts).toLocaleTimeString("zh-CN", { hour12: false });
}

async function clearLog(): Promise<void> {
  await ctx.bus.request("voice-control:_:log:clear", {}).catch(() => undefined);
}

// ── Prompt tab ─────────────────────────────────────────────────────────
const promptView = ref<PromptView | null>(null);
const systemTemplate = ref("");
const summaryTemplate = ref("");
const previewText = ref("");
const promptStatus = ref("");
const promptStatusError = ref(false);
const savingPrompt = ref(false);

async function loadPrompt(): Promise<void> {
  try {
    const view = (await ctx.bus.request("voice-control:_:prompt", {
      text: previewText.value,
    })) as PromptView;
    promptView.value = view;
    // Don't clobber in-flight edits on refresh.
    if (systemTemplate.value === "") systemTemplate.value = view.system_template;
    if (summaryTemplate.value === "") summaryTemplate.value = view.summary_template;
    promptStatus.value = "";
  } catch (error) {
    promptStatus.value = `读取失败：${String(error)}`;
    promptStatusError.value = true;
  }
}

async function savePrompt(): Promise<void> {
  if (savingPrompt.value) return;
  savingPrompt.value = true;
  promptStatus.value = "";
  try {
    await ctx.bus.request("config:_:set", {
      plugin: "plugins.voice-control", key: "system_template", value: systemTemplate.value,
    });
    await ctx.bus.request("config:_:set", {
      plugin: "plugins.voice-control", key: "summary_template", value: summaryTemplate.value,
    });
    promptStatus.value = "已保存，下次调用即生效";
    promptStatusError.value = false;
    await loadPrompt();
  } catch (error) {
    promptStatus.value = `保存失败：${String(error)}`;
    promptStatusError.value = true;
  } finally {
    savingPrompt.value = false;
  }
}

function restoreDefaults(): void {
  if (promptView.value === null) return;
  systemTemplate.value = promptView.value.defaults.system_template;
  summaryTemplate.value = promptView.value.defaults.summary_template;
}

// ── 功能目录 tab ───────────────────────────────────────────────────────
const catalog = ref<CatalogEntry[]>([]);
const catalogStatus = ref("");

async function loadCatalog(): Promise<void> {
  try {
    const result = (await ctx.bus.request("voice-control:_:catalog", {})) as { entries?: CatalogEntry[] };
    catalog.value = result.entries ?? [];
    catalogStatus.value = "";
  } catch (error) {
    catalogStatus.value = `读取失败：${String(error)}`;
  }
}

const catalogByPlugin = computed(() => {
  const groups = new Map<string, CatalogEntry[]>();
  for (const entry of catalog.value) {
    const list = groups.get(entry.plugin) ?? [];
    list.push(entry);
    groups.set(entry.plugin, list);
  }
  return [...groups.entries()];
});

function selectTab(next: Tab): void {
  tab.value = next;
  if (next === "prompt") void loadPrompt();
  if (next === "catalog") void loadCatalog();
}

onMounted(() => {
  // The log arrives via the retained mailbox subscription; nothing to fetch.
});
</script>

<template>
  <div class="vc-pane">
    <div class="vc-header">
      <div class="btn-group btn-group-sm" role="group">
        <button type="button" class="btn" :class="tab === 'log' ? 'btn-secondary' : 'btn-outline-secondary'" @click="selectTab('log')">日志</button>
        <button type="button" class="btn" :class="tab === 'prompt' ? 'btn-secondary' : 'btn-outline-secondary'" @click="selectTab('prompt')">Prompt</button>
        <button type="button" class="btn" :class="tab === 'catalog' ? 'btn-secondary' : 'btn-outline-secondary'" @click="selectTab('catalog')">功能目录</button>
      </div>
      <span class="small" :class="phase === 'off' ? 'text-secondary' : 'text-success'">● {{ phaseLabel }}</span>
      <button
        v-if="tab === 'log'"
        type="button"
        class="btn btn-sm btn-outline-danger ms-auto"
        title="清空交互日志"
        @click="clearLog"
      >清空</button>
      <button
        v-if="tab === 'prompt'"
        type="button"
        class="btn btn-sm btn-outline-secondary ms-auto"
        title="重新渲染预览"
        @click="loadPrompt"
      >刷新预览</button>
      <button
        v-if="tab === 'catalog'"
        type="button"
        class="btn btn-sm btn-outline-secondary ms-auto"
        title="重新获取合并目录"
        @click="loadCatalog"
      >刷新</button>
    </div>

    <!-- 日志 -->
    <div v-if="tab === 'log'" class="vc-body">
      <div v-if="logReversed.length === 0" class="small text-secondary">暂无记录。开启语音控制（Dock 底部耳机按钮 / Ctrl+Shift+M）后，每轮对话都会记录在这里。</div>
      <div v-for="(entry, i) in logReversed" :key="entry.ts + '-' + i" class="vc-entry">
        <div v-if="entry.kind === 'event'" class="small text-secondary">
          <span class="vc-time">{{ fmtTime(entry.ts) }}</span> {{ entry.detail }}
        </div>
        <template v-else>
          <div class="small text-secondary vc-entry-head">
            <span class="vc-time">{{ fmtTime(entry.ts) }}</span>
            <span v-if="entry.phase" class="vc-badge">{{ entry.phase }}</span>
            <span class="vc-badge" :class="entry.ok ? 'vc-ok' : 'vc-fail'">{{ entry.ok ? "ok" : "fail" }}</span>
            <span v-if="entry.llm_ms">LLM {{ entry.llm_ms }}ms</span>
            <span v-if="entry.invoke_ms">执行 {{ entry.invoke_ms }}ms</span>
          </div>
          <div class="vc-line"><span class="vc-role">用户</span>{{ entry.transcript }}</div>
          <div class="vc-line"><span class="vc-role">助手</span>{{ entry.say }}</div>
          <div v-if="entry.entry_id" class="vc-line small">
            <span class="vc-role">动作</span>{{ entry.entry_title || entry.entry_id }}
            <span class="text-secondary">（{{ entry.entry_id }} → {{ entry.channel }}）</span>
          </div>
          <div v-for="(effect, j) in entry.effects ?? []" :key="j" class="vc-line small">
            <span class="vc-role">效果</span>{{ effect.type }}
            <span class="text-secondary">{{ effect.pane_type ?? "" }} {{ effect.instance_id ?? "" }}</span>
          </div>
          <details v-if="entry.llm_raw || entry.detail" class="small">
            <summary class="text-secondary">细节</summary>
            <div v-if="entry.detail" class="text-danger">{{ entry.detail }}</div>
            <pre v-if="entry.llm_raw" class="vc-raw">{{ entry.llm_raw }}</pre>
          </details>
        </template>
      </div>
    </div>

    <!-- Prompt -->
    <div v-if="tab === 'prompt'" class="vc-body">
      <div class="small text-secondary mb-2">
        占位符：系统模板用 <code v-text="'{{catalog_section}}'"></code>（注入合并后的功能目录 JSON）；摘要模板用 <code v-text="'{{summary}}'"></code> 与 <code v-text="'{{turns}}'"></code>。保存后下一次调用即生效（后端逐调用重读 config-store）。
      </div>
      <label class="form-label small w-100">系统模板（每轮对话的 system prompt）
        <textarea v-model="systemTemplate" class="form-control form-control-sm font-monospace mt-1" rows="12" spellcheck="false"></textarea>
      </label>
      <label class="form-label small w-100 mt-2">摘要模板（上下文超预算时折叠旧轮次）
        <textarea v-model="summaryTemplate" class="form-control form-control-sm font-monospace mt-1" rows="6" spellcheck="false"></textarea>
      </label>
      <div class="d-flex align-items-center gap-2 mt-2">
        <button type="button" class="btn btn-sm btn-primary" :disabled="savingPrompt" @click="savePrompt">{{ savingPrompt ? "保存中…" : "保存" }}</button>
        <button type="button" class="btn btn-sm btn-outline-secondary" title="填入内置默认值（仍需保存）" @click="restoreDefaults">恢复默认</button>
        <span class="small" :class="promptStatusError ? 'text-danger' : 'text-secondary'">{{ promptStatus }}</span>
      </div>
      <hr>
      <div class="d-flex align-items-center gap-2 mb-2">
        <input v-model="previewText" type="text" class="form-control form-control-sm" placeholder="示例转写文本（预览用，可空）" @keyup.enter="loadPrompt">
      </div>
      <div v-if="promptView" class="small">
        <div class="text-secondary mb-1">
          当前会话：摘要 {{ promptView.summary === "" ? "（空）" : "" }}{{ promptView.summary }} · 历史 {{ promptView.history.length }} 条
        </div>
        <div v-for="(message, i) in promptView.messages" :key="i" class="vc-msg">
          <div class="text-secondary">{{ message.role }}</div>
          <pre class="vc-raw">{{ message.content }}</pre>
        </div>
      </div>
    </div>

    <!-- 功能目录 -->
    <div v-if="tab === 'catalog'" class="vc-body">
      <div v-if="catalogStatus" class="small text-danger">{{ catalogStatus }}</div>
      <div v-if="catalogByPlugin.length === 0 && catalogStatus === ''" class="small text-secondary">
        目录为空——还没有插件发布语音条目（插件经 retained mailbox voice-catalog:_:&lt;plugin&gt; 上报）。
      </div>
      <div v-for="[plugin, entries] in catalogByPlugin" :key="plugin" class="vc-plugin">
        <div class="vc-plugin-name">{{ plugin }} <span class="text-secondary small">（{{ entries.length }} 条）</span></div>
        <div v-for="entry in entries" :key="entry.id" class="vc-entry">
          <div class="vc-line">
            <span class="vc-badge" :class="entry.kind === 'open_instance' ? 'vc-ok' : ''">{{ entry.kind }}</span>
            {{ entry.title }}
          </div>
          <div class="small text-secondary vc-line">id: {{ entry.id }} → {{ entry.channel }}</div>
          <div v-if="entry.description" class="small text-secondary vc-line">{{ entry.description }}</div>
          <div v-if="(entry.keywords ?? []).length > 0" class="small text-secondary vc-line">关键词：{{ (entry.keywords ?? []).join("、") }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.vc-pane {
  display: flex;
  flex-direction: column;
  height: 100%;
  padding: 0.5rem;
  gap: 0.5rem;
  overflow: hidden;
}
.vc-header {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-shrink: 0;
}
.vc-body {
  flex: 1;
  overflow-y: auto;
  min-height: 0;
}
.vc-entry {
  padding: 0.25rem 0;
  border-bottom: 1px solid var(--bs-border-color-translucent, rgba(128, 128, 128, 0.2));
}
.vc-entry-head {
  display: flex;
  gap: 0.5rem;
}
.vc-time {
  font-family: monospace;
}
.vc-line {
  padding-left: 0.25rem;
}
.vc-role {
  display: inline-block;
  min-width: 2.2rem;
  color: var(--bs-secondary-color, #888);
  font-size: 0.85em;
}
.vc-badge {
  font-size: 0.8em;
  padding: 0 0.3rem;
  border: 1px solid var(--bs-border-color-translucent, rgba(128, 128, 128, 0.4));
  border-radius: 0.25rem;
}
.vc-ok {
  color: var(--bs-success, #4caf50);
}
.vc-fail {
  color: var(--bs-danger, #e57373);
}
.vc-raw {
  white-space: pre-wrap;
  word-break: break-all;
  font-size: 0.8em;
  margin: 0.25rem 0 0;
  color: var(--bs-secondary-color, #888);
}
.vc-msg {
  margin-bottom: 0.5rem;
}
.vc-plugin {
  margin-bottom: 0.75rem;
}
.vc-plugin-name {
  font-weight: 600;
}
</style>
