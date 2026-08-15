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
const workspace = ref<Workspace | null>(null);
const chat = ref<Chat | null>(null);
const draft = ref("");
const selected = ref<string[]>([]);
const activeRoles = ref(new Set<string>());
const error = ref("");
const threadRef = ref<HTMLElement | null>(null);
const styleOpen = ref(false);

// Newest-first pagination (old-viewer parity): the pane loads one page of
// the newest messages plus the activity blocks covering that span; scrolling
// to the top pulls an older page and restores the scroll position.
const PAGE_SIZE = 50;
const OLDER_SCROLL_THRESHOLD = 96;
const loadingInitial = ref(true);
const loadingOlder = ref(false);
const hasOlder = ref(false);
const loadedLo = ref(0); // oldest loaded message created_at (ms); 0 = unbounded
const olderCursor = ref<{ ts: number; id: string } | null>(null);
let everLoaded = false;

interface Segment { id: string; kind: "text" | "activity"; ts: number; text?: string; block?: ChatBlock }
interface TimelineBox { key: string; kind: "user" | "role"; label: string; roleId: string; turnId: string; ts: number; segments: Segment[] }

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
        key: `u:${message.id}`, kind: "user", label: "You", roleId: "", turnId: "", ts: message.created_at,
        segments: [{ id: message.id, kind: "text", ts: message.created_at, text: message.text }],
      });
      continue;
    }
    let box = turns.get(message.turn_id);
    if (!box) {
      box = { key: `t:${message.turn_id}`, kind: "role", label: "", roleId: "", turnId: message.turn_id, ts: message.created_at, segments: [] };
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
      box = { key: `t:${block.turn_id}`, kind: "role", label: "", roleId: "", turnId: block.turn_id, ts: block.occurred_at, segments: [] };
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

/** Role's configured execution target: agent / provider / model (first enabled candidate). */
function roleTargetLabel(roleId: string): string {
  const ws = workspace.value;
  if (!ws || !roleId) return "";
  const role = ws.roles.find((item) => item.id === roleId);
  if (!role) return "";
  const policyId = role.routing_policy_id || ws.default_routing_policy_id;
  const policy = ws.routing_policies.find((item) => item.id === policyId);
  const candidate = policy?.candidates.find((item) => item.enabled);
  if (!candidate) return "";
  return [candidate.agent_id, candidate.provider_id, candidate.model_id].filter(Boolean).join(" / ");
}

interface CtxUsage { used: number; size: number }

/** Latest token_usage block per turn — those blocks are hidden from the
 *  timeline and surfaced here in the box header instead (old-viewer parity). */
const turnUsage = computed<Map<string, CtxUsage>>(() => {
  const latest = new Map<string, { usage: CtxUsage; ts: number }>();
  for (const block of blocks.value) {
    if (block.kind !== "token_usage") continue;
    const payload = parseBlockPayload(block);
    const used = typeof payload?.total_tokens === "number" ? payload.total_tokens : 0;
    const size = typeof payload?.model_context_window === "number" ? payload.model_context_window : 0;
    if (!used && !size) continue;
    const existing = latest.get(block.turn_id);
    if (!existing || block.occurred_at >= existing.ts) latest.set(block.turn_id, { usage: { used, size }, ts: block.occurred_at });
  }
  return new Map([...latest].map(([key, value]) => [key, value.usage]));
});

function compactTokenCount(value: number): string {
  const units = value >= 1_000_000 ? { size: 1_000_000, suffix: "M" } : { size: 1000, suffix: "K" };
  const scaled = value / units.size;
  return `${scaled.toFixed(scaled >= 100 ? 0 : 1).replace(/\.0$/, "")}${units.suffix}`;
}

function usageLabel(box: TimelineBox): string {
  const usage = turnUsage.value.get(box.turnId);
  if (!usage) return "";
  const parts: string[] = [];
  if (usage.used && usage.size) parts.push(`${((usage.used / usage.size) * 100).toFixed(1)}% ctx`);
  if (usage.used) parts.push(usage.size ? `${compactTokenCount(usage.used)} / ${compactTokenCount(usage.size)}` : compactTokenCount(usage.used));
  return parts.join(" · ");
}

function usageTitle(box: TimelineBox): string {
  const usage = turnUsage.value.get(box.turnId);
  if (!usage) return "";
  return `${usage.used.toLocaleString()} of ${usage.size ? usage.size.toLocaleString() : "?"} context tokens`;
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

// Display whitelist: actions (tool/file/command) plus thinking and tool
// results — the user wants those visible. `other` (raw protocol noise) and
// any unknown kind stay hidden entirely, even if they carry text.
const ACTIVITY_KINDS = new Set(["tool_call", "file_change", "command", "thinking", "tool_result"]);
function activityDisplayable(block: ChatBlock): boolean {
  return ACTIVITY_KINDS.has(block.kind);
}

async function load(): Promise<void> {
  loadingInitial.value = true;
  try {
    const list = await (ctx.bus.request("chat:_:chats:list", {
      chat_id: ctx.instanceId, include_messages: true, limit: PAGE_SIZE,
    }) as Promise<ChatList>);
    chat.value = list.chats.find((item) => item.id === ctx.instanceId) ?? null;
    const page = list.messages ?? [];
    messages.value = page;
    hasOlder.value = list.has_more ?? false;
    if (page.length > 0) {
      loadedLo.value = page[0].created_at;
      olderCursor.value = { ts: page[0].created_at, id: page[0].id };
    } else {
      loadedLo.value = 0;
      olderCursor.value = null;
    }
    const [blockList, workspaceData] = await Promise.all([
      ctx.bus.request("chat:_:blocks:list", { chat_id: ctx.instanceId, after: loadedLo.value }) as Promise<ChatBlockList>,
      ctx.bus.request("chat:_:workspace:get", {}) as Promise<Workspace>,
    ]);
    blocks.value = blockList.blocks ?? [];
    roles.value = workspaceData.roles;
    workspace.value = workspaceData;
    ctx.setChrome({
      title: chat.value?.name ?? "Chat",
      actions: [
        { id: "chat-style", title: "消息样式", icon: "bi-palette", run: () => { styleOpen.value = !styleOpen.value; } },
        { id: "chat-config", title: "聊天管理", icon: "bi-sliders", run: () => layout.openInstance("chat-manager", "main") },
      ],
    });
    // First open lands on the newest end (old-viewer parity); later reloads
    // keep the user's scroll position.
    if (!everLoaded) {
      everLoaded = true;
      await nextTick();
      const thread = threadRef.value;
      if (thread) thread.scrollTop = thread.scrollHeight;
    }
  } finally {
    loadingInitial.value = false;
  }
}

/** Merge-refresh: re-pull the newest page + block window and fold new items
 *  in without disturbing loaded older pages or the scroll position (used for
 *  activation / chat-list mutations, where a full reset would lose the
 *  user's place in a long history). */
async function refresh(): Promise<void> {
  if (loadingInitial.value) return; // initial load() already fetches everything
  const [list, blockList, workspaceData] = await Promise.all([
    ctx.bus.request("chat:_:chats:list", {
      chat_id: ctx.instanceId, include_messages: true, limit: PAGE_SIZE,
    }) as Promise<ChatList>,
    ctx.bus.request("chat:_:blocks:list", { chat_id: ctx.instanceId, after: loadedLo.value }) as Promise<ChatBlockList>,
    ctx.bus.request("chat:_:workspace:get", {}) as Promise<Workspace>,
  ]);
  chat.value = list.chats.find((item) => item.id === ctx.instanceId) ?? null;
  if (!chat.value) {
    messages.value = [];
    blocks.value = [];
    hasOlder.value = false;
    olderCursor.value = null;
    return;
  }
  const page = list.messages ?? [];
  const known = new Set(messages.value.map((item) => item.id));
  for (const item of page) if (!known.has(item.id)) messages.value.push(item);
  messages.value.sort((a, b) => a.created_at - b.created_at || a.id.localeCompare(b.id));
  if (loadedLo.value === 0 && messages.value.length > 0) {
    loadedLo.value = messages.value[0].created_at;
    olderCursor.value = { ts: messages.value[0].created_at, id: messages.value[0].id };
    hasOlder.value = list.has_more ?? false;
  }
  for (const block of blockList.blocks ?? []) {
    if (!blocks.value.some((item) => item.id === block.id)) blocks.value.push(block);
  }
  roles.value = workspaceData.roles;
  workspace.value = workspaceData;
  ctx.setChrome({
    title: chat.value?.name ?? "Chat",
    actions: [
      { id: "chat-style", title: "消息样式", icon: "bi-palette", run: () => { styleOpen.value = !styleOpen.value; } },
      { id: "chat-config", title: "聊天管理", icon: "bi-sliders", run: () => layout.openInstance("chat-manager", "main") },
    ],
  });
}

/** Load one older page (composite cursor) plus the blocks in the span it
 *  newly covers, then restore the scroll position (old-viewer parity). */
async function loadOlder(): Promise<void> {
  if (loadingInitial.value || loadingOlder.value || !hasOlder.value || !olderCursor.value) return;
  loadingOlder.value = true;
  const thread = threadRef.value;
  const previousScrollHeight = thread?.scrollHeight ?? 0;
  const previousScrollTop = thread?.scrollTop ?? 0;
  try {
    const list = await (ctx.bus.request("chat:_:chats:list", {
      chat_id: ctx.instanceId, include_messages: true,
      before: olderCursor.value.ts, before_id: olderCursor.value.id, limit: PAGE_SIZE,
    }) as Promise<ChatList>);
    const page = list.messages ?? [];
    if (page.length === 0) {
      hasOlder.value = false;
      return;
    }
    const newLo = page[0].created_at;
    const blockList = await (ctx.bus.request("chat:_:blocks:list", {
      chat_id: ctx.instanceId, after: newLo, before: loadedLo.value,
    }) as Promise<ChatBlockList>);
    const known = new Set(messages.value.map((item) => item.id));
    messages.value = [...page.filter((item) => !known.has(item.id)), ...messages.value];
    for (const block of blockList.blocks ?? []) {
      if (!blocks.value.some((item) => item.id === block.id)) blocks.value.push(block);
    }
    hasOlder.value = list.has_more ?? false;
    olderCursor.value = { ts: newLo, id: page[0].id };
    loadedLo.value = newLo;
    await nextTick();
    if (thread) thread.scrollTop = thread.scrollHeight - previousScrollHeight + previousScrollTop;
  } catch (cause) {
    error.value = errorText(cause);
  } finally {
    loadingOlder.value = false;
  }
}

function handleThreadScroll(): void {
  const thread = threadRef.value;
  if (!thread || thread.scrollTop > OLDER_SCROLL_THRESHOLD) return;
  void loadOlder();
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
  const refreshNow = (): void => { void refresh().catch(() => undefined); };
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
  ctx.bus.subscribe("chat:_:active", (frame) => {
    if (frame.value === ctx.instanceId) refreshNow();
  });
  window.addEventListener("viewer:chats-changed", refreshNow);
  ctx.onDispose(() => window.removeEventListener("viewer:chats-changed", refreshNow));
  void load().catch((cause) => { error.value = errorText(cause); });
});
</script>

<template>
  <section class="chat-pane d-flex flex-column h-100">
    <div ref="threadRef" class="chat-thread flex-grow-1 overflow-auto p-2" aria-live="polite" @scroll.passive="handleThreadScroll">
      <div v-if="messages.length && (loadingOlder || !hasOlder)" class="chat-history-boundary small text-secondary">
        <span v-if="loadingOlder" class="spinner-border spinner-border-sm me-1" aria-hidden="true" />
        <template v-if="loadingOlder">加载更早消息…</template>
        <template v-else>没有更多消息</template>
      </div>
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
            <span v-if="box.kind === 'role' && roleTargetLabel(box.roleId)" class="chat-meta-detail">{{ roleTargetLabel(box.roleId) }}</span>
            <span v-if="box.kind === 'role' && usageLabel(box)" class="chat-meta-detail" :title="usageTitle(box)">{{ usageLabel(box) }}</span>
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

/* Role target (agent / provider / model) and ctx usage in the box header —
 * low-key like the activity lines, no highlighting. */
.chat-meta-detail {
  color: color-mix(in srgb, var(--color-text-muted) 72%, transparent);
  flex: 0 1 auto;
  font-size: 10px;
  line-height: 1.2;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
  /* Intentionally unboxed (user direction): activity rows are low-key
     one-liners — no panel background/border, just a dimmed single line. */
  color: color-mix(in srgb, var(--color-text-muted) 72%, transparent);
  min-width: 0;
}

.chat-activity-summary {
  align-items: center;
  cursor: pointer;
  display: grid;
  font-size: 10px;
  gap: 4px;
  grid-template-columns: auto auto minmax(0, 1fr) auto auto;
  line-height: 1.3;
  list-style: none;
  min-height: 16px;
  min-width: 0;
  padding: 0 2px;
  user-select: none;
}

.chat-activity-summary::-webkit-details-marker {
  display: none;
}

.chat-activity-summary:hover {
  color: var(--color-text-muted);
}

.chat-activity-flat {
  cursor: default;
  grid-template-columns: auto auto minmax(0, 1fr) auto;
}

.chat-activity-flat:hover {
  color: color-mix(in srgb, var(--color-text-muted) 72%, transparent);
}

.chat-activity-icon {
  color: inherit;
  font-size: 9.5px;
}

.chat-activity-label {
  color: inherit;
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
  max-height: min(420px, 55vh);
  overflow: auto;
  padding: 2px 2px 4px 18px;
  user-select: text;
}

.chat-activity-body pre {
  background: color-mix(in srgb, var(--color-surface-muted) 35%, transparent);
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10.5px;
  margin: 0 0 4px;
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

.chat-history-boundary {
  padding: 6px 0;
  text-align: center;
  user-select: none;
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
