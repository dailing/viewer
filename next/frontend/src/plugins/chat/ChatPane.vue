<script setup lang="ts">
/**
 * Chat pane timeline rendering (framework v0.30, A.7): one box per turn —
 * user message boxes and per-role turn boxes. Inside a role turn box,
 * markdown text segments (from `messages`) and tool-activity rows (from
 * non-`agent_text` `message_blocks`) interleave strictly by time. Box header
 * carries the info strip: role icon + name, running status, time. Markdown
 * goes through renderMarkdown (markdown-it + KaTeX + hljs line numbers +
 * mermaid); styling follows the --markdown-* theme variables (markdownStyle
 * store, customizable via the 消息样式 panel).
 */
import { computed, inject, nextTick, onMounted, ref, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import { useLayoutStore } from "../../stores/layout";
import { useMarkdownStyleStore } from "../../stores/markdownStyle";
import type { MarkdownStyleOverrides } from "../../stores/markdownStyle";
import { renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import ComposerBox from "./ComposerBox.vue";
import type { Chat, ChatBlock, ChatBlockList, ChatList, ChatMessage, Role, Workspace } from "./types";
import { errorText } from "./types";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("ChatPane requires PluginPaneHost");
const ctx: PluginCtx = injectedCtx;
const layout = useLayoutStore();
const markdownStyle = useMarkdownStyleStore();

const messages = ref<ChatMessage[]>([]);
const blocks = ref<ChatBlock[]>([]);
const roles = ref<Role[]>([]);
const chat = ref<Chat | null>(null);
const draft = ref("");
const selected = ref<string[]>([]);
const activeRoles = ref(new Set<string>());
const error = ref("");
const threadRef = ref<HTMLElement | null>(null);
const styleOpen = ref(false);

interface Segment { id: string; kind: "text" | "activity"; ts: number; text?: string; block?: ChatBlock }
interface TimelineBox { key: string; kind: "user" | "role"; label: string; roleId: string; ts: number; segments: Segment[] }

const ACTIVITY_LABELS: Record<string, string> = {
  thinking: "Reasoning",
  tool_call: "Tool call",
  tool_result: "Tool result",
  file_change: "Edit",
  command: "Command",
  other: "Activity",
};

const timeline = computed<TimelineBox[]>(() => {
  const turns = new Map<string, TimelineBox>();
  const boxes: TimelineBox[] = [];
  for (const message of messages.value) {
    if (message.role === "user") {
      boxes.push({
        key: `u:${message.id}`, kind: "user", label: "You", roleId: "", ts: message.created_at,
        segments: [{ id: message.id, kind: "text", ts: message.created_at, text: message.text }],
      });
      continue;
    }
    let box = turns.get(message.turn_id);
    if (!box) {
      box = { key: `t:${message.turn_id}`, kind: "role", label: "", roleId: "", ts: message.created_at, segments: [] };
      turns.set(message.turn_id, box);
      boxes.push(box);
    }
    if (!box.label) box.label = message.sender.role_name ?? "Agent";
    if (!box.roleId) box.roleId = message.sender.role_id ?? "";
    box.ts = Math.min(box.ts, message.created_at);
    box.segments.push({ id: message.id, kind: "text", ts: message.created_at, text: message.text });
  }
  for (const block of blocks.value) {
    if (block.kind === "agent_text") continue; // text blocks render via messages
    if (!activityDisplayable(block)) continue; // drop empty noise rows
    let box = turns.get(block.turn_id);
    if (!box) {
      box = { key: `t:${block.turn_id}`, kind: "role", label: "", roleId: "", ts: block.occurred_at, segments: [] };
      turns.set(block.turn_id, box);
      boxes.push(box);
    }
    if (!box.label) box.label = block.role_name ?? "Agent";
    if (!box.roleId) box.roleId = block.role_id ?? "";
    box.ts = Math.min(box.ts, block.occurred_at);
    box.segments.push({ id: block.id, kind: "activity", ts: block.occurred_at, block });
  }
  for (const box of boxes) box.segments.sort((a, b) => a.ts - b.ts || a.id.localeCompare(b.id));
  return boxes.sort((a, b) => a.ts - b.ts || a.key.localeCompare(b.key));
});

/** Cached markdown HTML per text segment (recomputed when timeline data changes). */
const renderedHtml = computed<Map<string, string>>(() => {
  const map = new Map<string, string>();
  for (const box of timeline.value) {
    for (const segment of box.segments) {
      if (segment.kind === "text" && box.kind === "role") map.set(segment.id, renderMarkdown(segment.text ?? ""));
    }
  }
  return map;
});

watch(renderedHtml, () => {
  void nextTick(() => void renderMermaidIn(threadRef.value, "chat-mermaid"));
}, { flush: "post" });

const members = computed(() => roles.value.filter((role) => chat.value?.member_role_ids.includes(role.id)));

function formatTime(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function turnActive(box: TimelineBox): boolean {
  return box.roleId !== "" && activeRoles.value.has(box.roleId);
}

function activityIcon(block?: ChatBlock): string {
  switch (block?.kind) {
    case "thinking": return "bi-lightbulb";
    case "file_change": return "bi-pencil-square";
    case "command": return "bi-terminal";
    case "tool_result": return "bi-check-circle";
    default: return "bi-tools";
  }
}

function activityLabel(block?: ChatBlock): string {
  return ACTIVITY_LABELS[block?.kind ?? ""] ?? "Activity";
}

function parseBlockPayload(block?: ChatBlock): Record<string, unknown> | null {
  if (!block?.payload) return null;
  try {
    const value = JSON.parse(block.payload) as unknown;
    if (value && typeof value === "object" && Object.keys(value).length > 0) return value as Record<string, unknown>;
  } catch { /* tolerate malformed payloads */ }
  return null;
}

function activitySummary(block?: ChatBlock): string {
  const text = (block?.text ?? "").replace(/\s+/g, " ").trim();
  if (text) return text;
  const payload = parseBlockPayload(block);
  if (payload && typeof payload.name === "string" && payload.name) return payload.name;
  return activityLabel(block);
}

function blockPayloadPretty(block?: ChatBlock): string {
  const payload = parseBlockPayload(block);
  return payload ? JSON.stringify(payload, null, 2) : "";
}

function activityHasBody(segment: Segment): boolean {
  return Boolean(segment.block && (segment.block.text.trim() || blockPayloadPretty(segment.block)));
}

// Only action rows display (matches the old viewer's effective surface):
// tool/method calls, file changes, commands. Thinking, tool results, and
// the `other` catch-all (raw protocol noise) stay hidden unless they carry
// actual text — a bare JSON payload is not displayable.
const ACTIVITY_KINDS = new Set(["tool_call", "file_change", "command"]);
function activityDisplayable(block: ChatBlock): boolean {
  if (ACTIVITY_KINDS.has(block.kind)) return true;
  return block.text.trim() !== "";
}

async function load(): Promise<void> {
  const [list, blockList, workspace] = await Promise.all([
    ctx.bus.request("chat:_:chats:list", { chat_id: ctx.instanceId, include_messages: true }) as Promise<ChatList>,
    ctx.bus.request("chat:_:blocks:list", { chat_id: ctx.instanceId }) as Promise<ChatBlockList>,
    ctx.bus.request("chat:_:workspace:get", {}) as Promise<Workspace>,
  ]);
  chat.value = list.chats.find((item) => item.id === ctx.instanceId) ?? null;
  messages.value = list.messages ?? [];
  blocks.value = blockList.blocks ?? [];
  roles.value = workspace.roles;
  ctx.setChrome({
    title: chat.value?.name ?? "Chat",
    actions: [
      { id: "chat-style", title: "消息样式", icon: "bi-palette", run: () => { styleOpen.value = !styleOpen.value; } },
      { id: "chat-config", title: "聊天管理", icon: "bi-sliders", run: () => layout.openInstance("chat-manager", "main") },
    ],
  });
}

async function send(): Promise<void> {
  const message = draft.value.trim();
  if (message === "") return;
  error.value = "";
  try {
    const payload: Record<string, unknown> = { chat_id: ctx.instanceId, message };
    if (selected.value.length > 0) payload.role_ids = selected.value;
    const result = await ctx.bus.request("chat:_:dispatch", payload) as { role_ids: string[] };
    activeRoles.value = new Set([...activeRoles.value, ...result.role_ids]);
    draft.value = "";
  } catch (cause) {
    error.value = errorText(cause);
  }
}

async function stop(): Promise<void> {
  try {
    await ctx.bus.request("chat:_:stop", { chat_id: ctx.instanceId });
  } catch (cause) {
    error.value = errorText(cause);
  }
}

const STYLE_DEFAULTS: Required<MarkdownStyleOverrides> = {
  bodyFontSize: 15, bodyLineHeight: 1.65, bodyColor: "#404449", strongColor: "#1f4e79",
  linkColor: "#58749a", codeFontSize: 13, codeBackground: "#f5f5f5", syntaxBackground: "#f5f5f5",
  borderColor: "#e3e4e6",
};
const NUMBER_FIELDS = new Set<keyof MarkdownStyleOverrides>(["bodyFontSize", "bodyLineHeight", "codeFontSize"]);

function overrideValue(field: keyof MarkdownStyleOverrides): string {
  const value = markdownStyle.overrides[field];
  return value === undefined ? String(STYLE_DEFAULTS[field]) : String(value);
}

function setOverride(field: keyof MarkdownStyleOverrides, raw: string): void {
  if (raw === "") {
    markdownStyle.set(field, undefined);
    return;
  }
  if (NUMBER_FIELDS.has(field)) {
    const numeric = Number(raw);
    markdownStyle.set(field, Number.isFinite(numeric) && numeric > 0 ? numeric : undefined);
    return;
  }
  markdownStyle.set(field, raw);
}

onMounted(() => {
  const reload = (): void => { void load().catch(() => undefined); };
  ctx.bus.subscribe(`chat:${ctx.instanceId}:message`, (frame) => {
    const value = frame.value as ChatMessage;
    const index = messages.value.findIndex((item) => item.id === value.id);
    if (index >= 0) messages.value.splice(index, 1, value); else messages.value.push(value);
  });
  ctx.bus.subscribe(`chat:${ctx.instanceId}:block`, (frame) => {
    const value = frame.value as ChatBlock;
    if (!blocks.value.some((item) => item.id === value.id)) blocks.value.push(value);
  });
  ctx.bus.subscribe(`chat:${ctx.instanceId}:turn-completed`, (frame) => {
    const value = frame.value as { role_id: string };
    const next = new Set(activeRoles.value);
    next.delete(value.role_id);
    activeRoles.value = next;
  });
  ctx.bus.subscribe("chat:_:active", reload);
  window.addEventListener("viewer:chats-changed", reload);
  ctx.onDispose(() => window.removeEventListener("viewer:chats-changed", reload));
  void load().catch((cause) => { error.value = errorText(cause); });
});
</script>

<template>
  <section class="chat-pane d-flex flex-column h-100">
    <div ref="threadRef" class="chat-thread flex-grow-1 overflow-auto p-2" aria-live="polite">
      <article v-for="box in timeline" :key="box.key" class="chat-box" :class="box.kind === 'user' ? 'chat-box-user' : 'chat-box-role'">
        <div class="chat-box-top">
          <div class="chat-meta">
            <span class="chat-role-label">
              <i class="bi" :class="box.kind === 'user' ? 'bi-person' : 'bi-robot'" />
              {{ box.label }}
            </span>
            <span v-if="turnActive(box)" class="chat-turn-status">
              <span class="spinner-border spinner-border-sm" aria-hidden="true" /> running
            </span>
            <span class="chat-time">{{ formatTime(box.ts) }}</span>
          </div>
        </div>
        <div class="chat-box-body chat-timeline">
          <template v-for="segment in box.segments" :key="segment.id">
            <div v-if="segment.kind === 'text' && box.kind === 'user'" class="chat-user-text">{{ segment.text }}</div>
            <div
              v-else-if="segment.kind === 'text' && (segment.text ?? '').trim()"
              class="markdown-content chat-response-body"
              v-html="renderedHtml.get(segment.id) ?? ''"
            />
            <div v-else-if="segment.kind === 'activity'" class="chat-activity">
              <details v-if="activityHasBody(segment)" class="chat-activity-details">
                <summary class="chat-activity-summary">
                  <i class="bi chat-activity-icon" :class="activityIcon(segment.block)" />
                  <span class="chat-activity-label">{{ activityLabel(segment.block) }}</span>
                  <span class="chat-activity-text">{{ activitySummary(segment.block) }}</span>
                  <span class="chat-time">{{ formatTime(segment.ts) }}</span>
                  <i class="bi bi-chevron-right chat-activity-chevron" aria-hidden="true" />
                </summary>
                <div class="chat-activity-body">
                  <pre v-if="segment.block?.text">{{ segment.block.text }}</pre>
                  <pre v-if="blockPayloadPretty(segment.block)">{{ blockPayloadPretty(segment.block) }}</pre>
                </div>
              </details>
              <div v-else class="chat-activity-summary chat-activity-flat">
                <i class="bi chat-activity-icon" :class="activityIcon(segment.block)" />
                <span class="chat-activity-label">{{ activityLabel(segment.block) }}</span>
                <span class="chat-activity-text">{{ activitySummary(segment.block) }}</span>
                <span class="chat-time">{{ formatTime(segment.ts) }}</span>
              </div>
            </div>
          </template>
        </div>
      </article>
      <div v-if="activeRoles.size" class="small text-secondary px-1">
        <span class="spinner-border spinner-border-sm me-1" />{{ activeRoles.size }} role turn(s) active
      </div>
      <div v-if="!timeline.length" class="chat-empty">Write one message and dispatch it into this chat.</div>
    </div>
    <div v-if="styleOpen" class="chat-style-panel" @keydown.esc="styleOpen = false">
      <div class="chat-style-head">
        <span>消息样式</span>
        <button type="button" class="btn btn-sm btn-outline-secondary" @click="markdownStyle.reset()">恢复默认</button>
        <button type="button" class="btn btn-sm btn-outline-secondary" title="关闭" @click="styleOpen = false">
          <i class="bi bi-x-lg" />
        </button>
      </div>
      <label class="chat-style-field">
        <span>正文字号</span>
        <input type="number" min="10" max="24" step="1" :value="overrideValue('bodyFontSize')" @change="setOverride('bodyFontSize', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="chat-style-field">
        <span>正文行高</span>
        <input type="number" min="1.1" max="2.4" step="0.05" :value="overrideValue('bodyLineHeight')" @change="setOverride('bodyLineHeight', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="chat-style-field">
        <span>正文颜色</span>
        <input type="color" :value="overrideValue('bodyColor')" @input="setOverride('bodyColor', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="chat-style-field">
        <span>加粗颜色</span>
        <input type="color" :value="overrideValue('strongColor')" @input="setOverride('strongColor', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="chat-style-field">
        <span>链接颜色</span>
        <input type="color" :value="overrideValue('linkColor')" @input="setOverride('linkColor', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="chat-style-field">
        <span>代码字号</span>
        <input type="number" min="9" max="20" step="1" :value="overrideValue('codeFontSize')" @change="setOverride('codeFontSize', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="chat-style-field">
        <span>行内代码底色</span>
        <input type="color" :value="overrideValue('codeBackground')" @input="setOverride('codeBackground', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="chat-style-field">
        <span>代码块底色</span>
        <input type="color" :value="overrideValue('syntaxBackground')" @input="setOverride('syntaxBackground', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="chat-style-field">
        <span>边框颜色</span>
        <input type="color" :value="overrideValue('borderColor')" @input="setOverride('borderColor', ($event.target as HTMLInputElement).value)">
      </label>
    </div>
    <div class="composer-shell">
      <div v-if="error" class="small text-danger mb-1">{{ error }}</div>
      <ComposerBox
        v-model="draft"
        v-model:selected-role-ids="selected"
        :roles="members"
        :context-id="'chat:' + ctx.instanceId"
        :has-active-roles="activeRoles.size > 0"
        @send="send"
        @stop="stop"
      />
    </div>
  </section>
</template>

<style scoped>
.chat-pane {
  position: relative;
}

.chat-box {
  margin-bottom: 8px;
  min-width: 0;
}

.chat-box-top {
  align-items: center;
  color: var(--color-text-muted);
  display: flex;
  font-size: var(--font-size-ui);
  justify-content: space-between;
  min-width: 0;
  overflow: hidden;
  padding: 0 2px 2px;
}

.chat-meta {
  align-items: center;
  display: flex;
  flex: 1 1 auto;
  flex-wrap: nowrap;
  gap: 6px;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
}

.chat-role-label {
  align-items: center;
  display: inline-flex;
  font-weight: 700;
  gap: 4px;
  min-width: 0;
}

.chat-turn-status {
  align-items: center;
  color: var(--bs-primary);
  display: inline-flex;
  font-size: 10.5px;
  gap: 4px;
}

.chat-turn-status .spinner-border {
  height: 10px;
  width: 10px;
}

.chat-time {
  color: var(--color-text-muted);
  flex: 0 0 auto;
  font-size: 11px;
  line-height: 1.2;
}

.chat-box-body {
  background: var(--color-surface-muted);
  border: 0;
  border-radius: var(--radius-md);
  min-width: 0;
  overflow-x: hidden;
  padding: 8px 10px;
  user-select: text;
  width: 100%;
}

.chat-timeline {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.chat-user-text {
  overflow-wrap: anywhere;
  user-select: text;
  white-space: pre-wrap;
  word-break: break-word;
}

/* Compact markdown inside chat turns: sizes pin to the UI scale (colors and
   anything not listed still follow the markdown theme variables). */
.chat-response-body {
  --markdown-render-body-size: var(--font-size-ui);
  --markdown-render-h1-size: 20px;
  --markdown-render-h2-size: 17px;
  --markdown-render-h3-size: 15px;
  --markdown-render-h4-size: 14px;
  --markdown-render-paragraph-line-height: 1.48;
  --markdown-render-paragraph-size: var(--font-size-ui);
  --markdown-render-pre-padding: 8px;
  flex: 0 0 auto;
  max-width: 100%;
  min-width: 0;
  overflow-wrap: anywhere;
  user-select: text;
}

.chat-activity {
  min-width: 0;
}

.chat-activity-details {
  background: color-mix(in srgb, var(--color-surface-muted) 45%, transparent);
  border: 1px solid color-mix(in srgb, var(--color-border) 45%, transparent);
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  min-width: 0;
}

.chat-activity-summary {
  align-items: center;
  cursor: pointer;
  display: grid;
  font-size: 10.5px;
  gap: 5px;
  grid-template-columns: auto auto minmax(0, 1fr) auto auto;
  line-height: 1.3;
  list-style: none;
  min-height: 22px;
  min-width: 0;
  padding: 2px 6px;
  user-select: none;
}

.chat-activity-summary::-webkit-details-marker {
  display: none;
}

.chat-activity-summary:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.chat-activity-flat {
  cursor: default;
  grid-template-columns: auto auto minmax(0, 1fr) auto;
}

.chat-activity-flat:hover {
  background: transparent;
  color: var(--color-text-muted);
}

.chat-activity-icon {
  color: var(--color-text-muted);
}

.chat-activity-label {
  color: var(--color-text-muted);
  font-weight: 500;
  white-space: nowrap;
}

.chat-activity-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.chat-activity-chevron {
  font-size: 10px;
  transition: transform 120ms ease;
}

.chat-activity-details[open] .chat-activity-chevron {
  transform: rotate(90deg);
}

.chat-activity-body {
  border-top: 1px solid color-mix(in srgb, var(--color-border) 55%, transparent);
  max-height: min(420px, 55vh);
  overflow: auto;
  padding: 6px;
  user-select: text;
}

.chat-activity-body pre {
  background: var(--color-surface-raised);
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10.5px;
  margin: 0 0 6px;
  overflow: auto;
  padding: 6px;
  white-space: pre-wrap;
  word-break: break-word;
}

.chat-activity-body pre:last-child {
  margin-bottom: 0;
}

.chat-empty {
  color: var(--color-text-muted);
  font-size: var(--font-size-ui);
  padding: 12px 4px;
  text-align: center;
}

.chat-style-panel {
  background: var(--color-surface-raised);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.16);
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 10px;
  position: absolute;
  right: 8px;
  top: 8px;
  width: 220px;
  z-index: 20;
}

.chat-style-head {
  align-items: center;
  display: flex;
  font-size: var(--font-size-ui);
  font-weight: 700;
  gap: 6px;
  justify-content: space-between;
}

.chat-style-head > span {
  flex: 1 1 auto;
}

.chat-style-field {
  align-items: center;
  display: grid;
  font-size: var(--font-size-ui);
  gap: 8px;
  grid-template-columns: 1fr auto;
}

.chat-style-field > span {
  color: var(--color-text-muted);
}

.chat-style-field input[type="number"] {
  width: 72px;
}

.chat-style-field input[type="color"] {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  height: 22px;
  padding: 1px;
  width: 42px;
}

.composer-shell {
  flex: 0 0 auto;
  min-width: 0;
  padding: 0;
  width: 100%;
  z-index: 5;
}
</style>
