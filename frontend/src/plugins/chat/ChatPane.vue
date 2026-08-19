<script setup lang="ts">
/**
 * Chat pane timeline rendering (framework v0.30, A.7): one box per turn —
 * user message boxes and per-role turn boxes. Inside a role turn box,
 * markdown text segments (from `messages`) and tool-activity rows (from
 * non-`agent_text` `message_blocks`) interleave strictly by time. Box header
 * carries the info strip: role icon + name, running status, time. Markdown
 * goes through renderMarkdown (markdown-it + KaTeX + hljs line numbers +
 * mermaid); styling follows the --markdown-* theme variables (markdownStyle
 * store, customizable via the settings pane).
 */
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import { useChatSettingsStore } from "../../stores/chatSettings";
import { renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import ComposerBox from "./ComposerBox.vue";
import { loadEntry, removeEntry, saveEntry } from "./chatCache";
import type { ChatCacheEntry, MessageCursor } from "./chatCache";
import type { Chat, ChatBlock, ChatBlockList, ChatList, ChatMessage, Role, Workspace } from "./types";
import { errorText } from "./types";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("ChatPane requires PluginPaneHost");
const ctx: PluginCtx = injectedCtx;
const chatSettings = useChatSettingsStore();

const messages = ref<ChatMessage[]>([]);
const blocks = ref<ChatBlock[]>([]);
const roles = ref<Role[]>([]);
const workspace = ref<Workspace | null>(null);
const chat = ref<Chat | null>(null);
const selected = ref<string[]>([]);
// In-flight turns of this chat, keyed by turn id. Seeded from the
// chats:list running_turns snapshot (survives pane reloads/remounts) and
// updated live by the chat:_:turn started/completed feed. Per-turn binding
// keeps parallel send-now turns of the same role individually marked — and
// individually stoppable.
const runningTurns = ref(new Map<string, { roleId: string }>());
const runningRoleIds = computed(() => new Set([...runningTurns.value.values()].map((turn) => turn.roleId)));
// Optimistic per-role placeholder boxes: created the moment dispatch returns
// so a response box appears immediately, and resolved when the turn's first
// live message/block arrives (the real turn box takes over) or the turn ends.
const pendingTurns = ref(new Map<string, { key: string; roleId: string; label: string; ts: number }>());
// Optimistic user-send boxes: shown the moment the user hits send (with a
// "sending" marker), annotated with the routed agent/provider when dispatch
// returns, and replaced by the real user message once the bus delivers it.
interface PendingSend { key: string; text: string; ts: number; sending: boolean; routed: string; failed: string }
const pendingSends = ref<PendingSend[]>([]);
// Routed label transferred onto the real user message (by id) when it lands,
// so the arrow survives the optimistic box being replaced.
const routedLabels = new Map<string, string>();
const error = ref("");
const threadRef = ref<HTMLElement | null>(null);
const messageEndRef = ref<HTMLElement | null>(null);

// Newest-first pagination (old-viewer parity): the pane loads one page of
// the newest messages plus the activity blocks covering that span; scrolling
// to the top pulls an older page and restores the scroll position.
const PAGE_SIZE = 50;
const OLDER_SCROLL_THRESHOLD = 96;
const NEWER_SCROLL_THRESHOLD = 96;
// Render window (2026-08-19): the DOM holds only a bounded slice of history.
// Attached to the live edge the window is the newest WINDOW_MAX_MESSAGES
// messages and streaming evicts the oldest pages; scrolling far up detaches
// from the live edge (live frames stop merging) and both edges stay capped —
// scrolling back down re-fetches newer pages until the edge reattaches.
// Adjustable: raise for more resident DOM, lower for less CPU.
const WINDOW_MAX_MESSAGES = 300;
// Scroll-up distance (in viewports) at which the pane detaches from the live
// edge. Only genuine upward scrolling detaches — content growth under a
// stationary viewport never does.
const LIVE_DETACH_VIEWPORTS = 1.5;
// Streaming batch: inbound message/block frames are coalesced into one
// reactive update per interval instead of one per frame (a hot stream
// publishes many frames per second; each used to rebuild the whole timeline).
const STREAM_FLUSH_MS = 100;
const loadingInitial = ref(true);
const loadingOlder = ref(false);
const loadingNewer = ref(false);
const hasOlder = ref(false);
const loadedLo = ref(0); // oldest loaded message created_at (ms); 0 = unbounded
const olderCursor = ref<{ ts: number; id: string } | null>(null);
// Detached-window state: hasNewer = the live edge is beyond the loaded
// window; loadedHi = cursor of the newest loaded message (loadNewer start).
const hasNewer = ref(false);
const loadedHi = ref<MessageCursor | null>(null);
let everLoaded = false;
let userScrolled = false; // manual scroll wins over the cache-hit auto-scroll
let lastProgrammaticScrollAt = 0; // suppresses the scroll-event echo of scrollThreadTop
let lastObservedScrollTop = 0; // direction tracking for live-edge detach

interface Segment { id: string; kind: "text" | "activity"; ts: number; text?: string; block?: ChatBlock }
interface TimelineBox { key: string; kind: "user" | "role"; label: string; roleId: string; turnId: string; ts: number; segments: Segment[]; pending?: boolean; sending?: boolean; routed?: string; failed?: string }

const ACTIVITY_LABELS: Record<string, string> = {
  thinking: "Reasoning",
  tool_call: "Tool call",
  tool_result: "Tool result",
  file_change: "Edit",
  command: "Command",
  error: "Error",
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
        routed: routedLabels.get(message.id) ?? "",
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
  // Optimistic placeholders for just-dispatched roles ride the same timeline;
  // their ts (dispatch time) keeps them at the end until real events land.
  for (const pending of pendingTurns.value.values()) {
    boxes.push({ key: pending.key, kind: "role", label: pending.label, roleId: pending.roleId, turnId: "", ts: pending.ts, segments: [], pending: true });
  }
  // Optimistic user-send boxes ride the same timeline at the end.
  for (const sent of pendingSends.value) {
    boxes.push({
      key: sent.key, kind: "user", label: "You", roleId: "", turnId: "", ts: sent.ts,
      segments: [{ id: sent.key, kind: "text", ts: sent.ts, text: sent.text }],
      sending: sent.sending, routed: sent.routed, failed: sent.failed,
    });
  }
  // Millisecond ties resolve user-before-role: a request is always the cause
  // of the response, so at equal ts the request box rides on top. (The bare
  // key compare ordered `pending:`/`t:` before `u:`/`send:` — a fast explicit
  // dispatch landing in the same ms flashed the response above the request.)
  const kindRank = (box: TimelineBox): number => (box.kind === "user" ? 0 : 1);
  return boxes.sort((a, b) => a.ts - b.ts || kindRank(a) - kindRank(b) || a.key.localeCompare(b.key));
});

/**
 * Per-segment markdown HTML, rendered lazily and cached by message id.
 * Incremental (not a computed over the whole timeline): loading an older page
 * renders only the newly added segments instead of re-running renderMarkdown
 * on every loaded message, which made typing and paging stutter once a long
 * history was in the DOM. Entries are invalidated when the text changes
 * (streaming updates replace the row) and pruned to a bounded size.
 */
const markdownCache = new Map<string, { text: string; html: string }>();
const MARKDOWN_CACHE_MAX = 3000;
function renderedHtmlFor(id: string, text: string): string {
  const hit = markdownCache.get(id);
  if (hit !== undefined && hit.text === text) return hit.html;
  const html = renderMarkdown(text);
  markdownCache.set(id, { text, html });
  if (markdownCache.size > MARKDOWN_CACHE_MAX) {
    const oldest = markdownCache.keys().next().value;
    if (oldest !== undefined) markdownCache.delete(oldest);
  }
  return html;
}

let mermaidRenderTimer: ReturnType<typeof setTimeout> | null = null;
let mermaidRenderDeadline = 0;
let streamingMessageId = "";

/** Schedule a mermaid render with a max-wait guarantee. During streaming the
 *  deadline stays anchored at the earliest requested time so it fires at most
 *  once per maxWait interval, but message boundaries are allowed to move the
 *  deadline earlier (see renderMermaidAtBoundary). */
function scheduleMermaidRender(maxWait = 500): void {
  const now = Date.now();
  const deadline = mermaidRenderDeadline || now + maxWait;
  mermaidRenderDeadline = Math.min(deadline, now + maxWait);
  if (mermaidRenderTimer !== null) clearTimeout(mermaidRenderTimer);
  mermaidRenderTimer = setTimeout(() => {
    mermaidRenderTimer = null;
    mermaidRenderDeadline = 0;
    void renderMermaidIn(threadRef.value, "chat-mermaid");
  }, Math.max(0, mermaidRenderDeadline - now));
}

/** Called when the current assistant text message is sealed (new message id
 *  arrived, a non-text block arrived, or the turn completed). Use a short
 *  delay to batch a rapid sequence of boundaries while still rendering as soon
 *  as the message is fully known. */
function renderMermaidAtBoundary(): void {
  scheduleMermaidRender(50);
}

watch(timeline, () => {
  scheduleMermaidRender(500);
}, { flush: "post" });

onBeforeUnmount(() => {
  if (mermaidRenderTimer !== null) {
    clearTimeout(mermaidRenderTimer);
    mermaidRenderTimer = null;
  }
  if (streamFlushTimer !== null) {
    clearTimeout(streamFlushTimer);
    streamFlushTimer = null;
  }
  flushStream(); // persist the last pending batch into the session cache
  for (const timer of confirmTimers.values()) clearTimeout(timer);
  confirmTimers.clear();
  mermaidRenderDeadline = 0;
  streamingMessageId = "";
});

const members = computed(() => roles.value.filter((role) => chat.value?.member_role_ids.includes(role.id)));

function formatTime(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

/** Running state binds to the turn, not the role: each in-flight turn's
 *  box shows the indicator (parallel send-now turns of one role included),
 *  historical boxes never do. Optimistic placeholder boxes (no turn id yet)
 *  count as active while their role has a dispatched/running turn. */
function turnActive(box: TimelineBox): boolean {
  if (box.roleId === "") return false;
  if (box.turnId !== "") return runningTurns.value.has(box.turnId);
  return box.pending === true && (pendingTurns.value.has(box.roleId) || runningRoleIds.value.has(box.roleId));
}

/** Drop a role's optimistic placeholder once its turn produces visible
 *  output (or ends) — the real turn box / final state takes over. */
function resolvePendingTurn(roleId: string | undefined): void {
  if (!roleId || !pendingTurns.value.has(roleId)) return;
  const next = new Map(pendingTurns.value);
  next.delete(roleId);
  pendingTurns.value = next;
}

/** Role's configured execution target: agent / provider / model (first enabled candidate).
 *  Honors chat-level routing override before falling back to the role's own
 *  policy and then the workspace default. */
function roleTargetLabel(roleId: string): string {
  const ws = workspace.value;
  if (!ws || !roleId) return "";
  const role = ws.roles.find((item) => item.id === roleId);
  if (!role) return "";
  const policyId = chat.value?.role_routing_policy_overrides?.[roleId] || role.routing_policy_id || ws.default_routing_policy_id;
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
    case "error": return "bi-exclamation-triangle";
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
// results — the user wants those visible. `error` rows are emitted by the
// chat plugin itself when a turn fails or stops abnormally. `other` (raw
// protocol noise) and any unknown kind stay hidden entirely, even if they
// carry text.
const ACTIVITY_KINDS = new Set(["tool_call", "file_change", "command", "thinking", "tool_result", "error"]);
function activityDisplayable(block: ChatBlock): boolean {
  return ACTIVITY_KINDS.has(block.kind);
}

/** Capture the current pane state into a cache entry. Array references are
 *  shared with the component, so the data stays in sync; this only records
 *  the metadata and touches the LRU. */
function writeBack(): void {
  let blockHigh = 0;
  for (const block of blocks.value) if (block.occurred_at > blockHigh) blockHigh = block.occurred_at;
  const entry: ChatCacheEntry = {
    chat: chat.value,
    messages: messages.value,
    blocks: blocks.value,
    roles: roles.value,
    workspace: workspace.value,
    hasOlder: hasOlder.value,
    olderCursor: olderCursor.value ? { ...olderCursor.value } : null,
    loadedLo: loadedLo.value,
    blockHigh,
  };
  saveEntry(ctx.instanceId, entry);
}

/** Restore pane state from a cache entry without any network traffic. */
function hydrate(entry: ChatCacheEntry): void {
  messages.value = entry.messages;
  blocks.value = entry.blocks;
  roles.value = entry.roles;
  workspace.value = entry.workspace;
  chat.value = entry.chat;
  hasOlder.value = entry.hasOlder;
  olderCursor.value = entry.olderCursor ? { ...entry.olderCursor } : null;
  loadedLo.value = entry.loadedLo;
  setChrome();
}

function setChrome(): void {
  ctx.setChrome({ title: chat.value?.name ?? "Chat" });
}

// ---------------------------------------------------------------------------
// Render window: bounded resident DOM (see constants above). The loaded
// slice [loadedLo .. loadedHi] is contiguous; evicted pages stay on the
// server and are transparently re-fetched by the existing scroll triggers.
// ---------------------------------------------------------------------------

/** True when a live frame sits beyond the detached window's upper edge —
 *  such frames are skipped while detached (loadNewer/reattach fills them). */
function beyondWindowEdge(ts: number, id: string): boolean {
  const hi = loadedHi.value;
  if (!hasNewer.value || hi === null) return false;
  return ts > hi.ts || (ts === hi.ts && id > hi.id);
}

/** Drop the oldest page from the window and compensate the scroll position
 *  by the removed height so the viewport content does not move. */
function evictTopPage(): void {
  const thread = threadRef.value;
  const beforeHeight = thread?.scrollHeight ?? 0;
  const beforeTop = thread?.scrollTop ?? 0;
  messages.value.splice(0, Math.min(PAGE_SIZE, messages.value.length));
  const oldest = messages.value[0];
  loadedLo.value = oldest?.created_at ?? 0;
  olderCursor.value = oldest ? { ts: oldest.created_at, id: oldest.id } : null;
  hasOlder.value = true; // evicted pages are re-fetchable from the server
  if (oldest) blocks.value = blocks.value.filter((block) => block.occurred_at >= oldest.created_at);
  if (thread && beforeHeight > 0) {
    void nextTick(() => {
      const removed = beforeHeight - thread.scrollHeight;
      if (removed !== 0) scrollThreadTop(Math.max(0, beforeTop - removed));
    });
  }
}

/** Drop the newest page from the window (user is reading far above). The
 *  removed content is below the viewport, so no scroll compensation is
 *  needed; the bottom trigger re-fetches it on the way down. */
function evictBottomPage(): void {
  messages.value.splice(Math.max(0, messages.value.length - PAGE_SIZE));
  const newest = messages.value[messages.value.length - 1];
  loadedHi.value = newest ? { ts: newest.created_at, id: newest.id } : null;
  hasNewer.value = true;
  if (newest) blocks.value = blocks.value.filter((block) => block.occurred_at <= newest.created_at);
}

/** Keep the resident window at or below WINDOW_MAX_MESSAGES, evicting the
 *  `prefer`red edge first. The edge follows browse intent: streaming and
 *  downward catch-up shed the top, upward history reading sheds the bottom.
 *  Attached to the live edge the top is always the victim (the live tail is
 *  sacred). */
function enforceWindow(prefer: "top" | "bottom"): void {
  if (loadingOlder.value || loadingNewer.value) return; // mid-flight page merge
  while (messages.value.length > WINDOW_MAX_MESSAGES) {
    if (!hasNewer.value || prefer === "top") evictTopPage(); else evictBottomPage();
  }
}

// ---------------------------------------------------------------------------
// Streaming batch: inbound frames are queued and merged once per
// STREAM_FLUSH_MS instead of once per frame, so a hot stream costs a bounded
// number of timeline rebuilds per second (and each rebuild is bounded by the
// render window above). All per-frame side effects run inside the flush.
// ---------------------------------------------------------------------------
const pendingMessageUpserts = new Map<string, ChatMessage>();
const pendingBlockUpserts = new Map<string, ChatBlock>();
let streamFlushTimer: ReturnType<typeof setTimeout> | null = null;

function scheduleStreamFlush(): void {
  if (streamFlushTimer !== null) return;
  streamFlushTimer = setTimeout(() => {
    streamFlushTimer = null;
    flushStream();
  }, STREAM_FLUSH_MS);
}

/** Merge one live message into the window (called only from flushStream). */
function mergeMessage(value: ChatMessage): void {
  if (value.role === "assistant" && value.id !== streamingMessageId) {
    if (streamingMessageId !== "") renderMermaidAtBoundary();
    streamingMessageId = value.id;
  }
  const index = messages.value.findIndex((item) => item.id === value.id);
  if (index >= 0) messages.value.splice(index, 1, value); else messages.value.push(value);
  if (value.role === "user") {
    // The real user message supersedes its optimistic send box: transfer
    // the routed label onto the real box, then drop the placeholder.
    let sendIndex = pendingSends.value.findIndex((item) => item.text === value.text.trim());
    if (sendIndex < 0) sendIndex = pendingSends.value.findIndex((item) => !item.sending && !item.failed);
    if (sendIndex >= 0) {
      const routed = pendingSends.value[sendIndex].routed;
      if (routed) routedLabels.set(value.id, routed);
      pendingSends.value.splice(sendIndex, 1);
    }
  }
  resolvePendingTurn(value.sender?.role_id);
  if (scrollOnNextUserMessage && value.role === "user") {
    scrollOnNextUserMessage = false;
    void nextTick(() => scrollThreadToMessageEnd());
  }
}

/** Merge one live block into the window (called only from flushStream). */
function mergeBlock(value: ChatBlock): void {
  // A non-text block seals the current assistant text message.
  if (streamingMessageId !== "") {
    streamingMessageId = "";
    renderMermaidAtBoundary();
  }
  // Merged streaming blocks (thinking/agent_text) republish with the same
  // id as their text grows — upsert by id, never append-if-absent.
  const index = blocks.value.findIndex((item) => item.id === value.id);
  if (index >= 0) blocks.value.splice(index, 1, value); else blocks.value.push(value);
  resolvePendingTurn(value.role_id);
}

function flushStream(): void {
  if (streamFlushTimer !== null) {
    clearTimeout(streamFlushTimer);
    streamFlushTimer = null;
  }
  if (pendingMessageUpserts.size === 0 && pendingBlockUpserts.size === 0) return;
  const messageUpserts = [...pendingMessageUpserts.values()];
  const blockUpserts = [...pendingBlockUpserts.values()];
  pendingMessageUpserts.clear();
  pendingBlockUpserts.clear();
  for (const value of messageUpserts) mergeMessage(value);
  for (const value of blockUpserts) mergeBlock(value);
  writeBack();
  enforceWindow("top");
}

/** Assign scrollTop and remember the timestamp so the resulting scroll event
 *  is not mistaken for manual user scrolling. */
function scrollThreadTop(top: number): void {
  lastProgrammaticScrollAt = Date.now();
  const thread = threadRef.value;
  if (thread) {
    thread.scrollTop = top;
    lastObservedScrollTop = top;
  }
}

/** Scroll so the message-end anchor sits at the viewport's lower edge, with
 *  the virtual space (when enabled) below it — old-viewer parity. Used for
 *  initial loads and after the user sends a query; streaming updates never
 *  auto-scroll. With virtual space off this equals the absolute end. */
function scrollThreadToMessageEnd(): void {
  const thread = threadRef.value;
  if (!thread) return;
  const end = messageEndRef.value;
  if (!end) {
    scrollThreadTop(thread.scrollHeight);
    return;
  }
  const delta = end.getBoundingClientRect().top - thread.getBoundingClientRect().top;
  scrollThreadTop(Math.max(0, thread.scrollTop + delta - thread.clientHeight));
}

// Set by send(): the next user message landing in the thread is the one the
// user just dispatched, so the thread scrolls it into view once it arrives.
let scrollOnNextUserMessage = false;

/** Fetch every block in [after, before), following the reply's truncation
 *  cursor (the backend caps one reply near the kernel's 1 MiB frame limit and
 *  reports truncated/next_after). Overlapping boundary blocks are upserted by
 *  id, so the result stays ordered with no duplicates. */
async function fetchBlocks(after: number, before = 0): Promise<ChatBlock[]> {
  const byId = new Map<string, ChatBlock>();
  let cursor = after;
  for (;;) {
    const list = await (ctx.bus.request("chat:_:blocks:list", {
      chat_id: ctx.instanceId, after: cursor, ...(before > 0 ? { before } : {}),
    }) as Promise<ChatBlockList>);
    for (const block of list.blocks ?? []) byId.set(block.id, block);
    if (!(list.truncated ?? false) || !list.next_after) break;
    cursor = list.next_after;
  }
  return [...byId.values()];
}

/** Adopt the backend's per-turn running snapshot (chats:list running_turns)
 *  so chips are correct after a pane reload/remount mid-turn; the live
 *  chat:_:turn feed takes over from there. */
function seedRunningTurns(list: ChatList): void {
  const next = new Map<string, { roleId: string }>();
  for (const turn of list.running_turns ?? []) next.set(turn.turn_id, { roleId: turn.role_id });
  runningTurns.value = next;
}

async function load(fresh = false): Promise<void> {
  loadingInitial.value = true;
  streamingMessageId = "";
  hasNewer.value = false; // a (re)load always lands on the live edge
  loadedHi.value = null;
  // Replacing the message list shrinks the DOM and the browser clamps
  // scrollTop, echoing a scroll event that would otherwise look like a
  // manual far-up scroll and instantly re-detach the fresh window.
  lastProgrammaticScrollAt = Date.now();
  lastObservedScrollTop = 0;
  try {
    // Session cache hit (v0.32): hydrate instantly, then merge only the
    // delta; the pane renders before any network round-trip. A `fresh` load
    // (jump-to-latest, detached refresh) skips the cache: the detached window
    // can sit arbitrarily far from the live edge, beyond what the bounded
    // delta refresh would close.
    const cached = fresh ? undefined : loadEntry(ctx.instanceId);
    if (cached) {
      hydrate(cached);
      enforceWindow("top"); // trim over-cap cache before the first paint
      loadingInitial.value = false;
      everLoaded = true;
      await nextTick();
      scrollThreadToMessageEnd();
      const exists = await refreshDelta();
      if (!exists) {
        // Chat was deleted while the pane was closed: clear and evict.
        messages.value = [];
        blocks.value = [];
        hasOlder.value = false;
        olderCursor.value = null;
        removeEntry(ctx.instanceId);
        return;
      }
      // Land on the message-end anchor once the delta has been folded in,
      // unless the user already started reading during the delta window.
      if (!userScrolled) scrollThreadToMessageEnd();
      return;
    }
    const list = await (ctx.bus.request("chat:_:chats:list", {
      chat_id: ctx.instanceId, include_messages: true, limit: PAGE_SIZE,
    }) as Promise<ChatList>);
    chat.value = list.chats.find((item) => item.id === ctx.instanceId) ?? null;
    seedRunningTurns(list);
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
    const [fetchedBlocks, workspaceData] = await Promise.all([
      fetchBlocks(loadedLo.value),
      ctx.bus.request("chat:_:workspace:get", {}) as Promise<Workspace>,
    ]);
    blocks.value = fetchedBlocks;
    roles.value = workspaceData.roles;
    workspace.value = workspaceData;
    setChrome();
    writeBack();
    // First open lands on the message-end anchor (old-viewer parity); a
    // fresh jump-to-latest reload does the same; other reloads keep the
    // user's scroll position.
    if (!everLoaded || fresh) {
      everLoaded = true;
      await nextTick();
      scrollThreadToMessageEnd();
    }
  } finally {
    loadingInitial.value = false;
  }
}

/** Merge-refresh (v0.32): fold in only what changed since the last fetch —
 *  messages at-or-newer than the newest loaded message (inclusive boundary:
 *  a still-streaming row's final text replaces the cached copy) and blocks
 *  newer than the cached max occurred_at. Used for activation / chat-list
 *  mutations; loaded older pages and the scroll position stay untouched.
 *  Returns false when the chat no longer exists. */
async function refreshDelta(): Promise<boolean> {
  const top = messages.value.length > 0 ? messages.value[messages.value.length - 1] : null;
  const blockAfter = blocks.value.reduce((max, block) => Math.max(max, block.occurred_at), 0);
  let chats: Chat[] = [];
  let firstHasMore = false;
  const fetched: ChatMessage[] = [];
  let cursor: MessageCursor | null = top ? { ts: top.created_at, id: top.id } : null;
  const incremental = cursor !== null;
  for (let pageCount = 0; pageCount < 3; pageCount++) {
    const list = await (ctx.bus.request("chat:_:chats:list", {
      chat_id: ctx.instanceId, include_messages: true,
      ...(cursor ? { after: cursor.ts, after_id: cursor.id } : {}),
      limit: PAGE_SIZE,
    }) as Promise<ChatList>);
    if (pageCount === 0) {
      chats = list.chats;
      firstHasMore = list.has_more ?? false;
      seedRunningTurns(list);
    }
    const page = list.messages ?? [];
    fetched.push(...page);
    // A no-cursor top page reports "older exist" as has_more — nothing newer
    // to chase; the incremental cursor page reports "even newer exist".
    if (!incremental || page.length === 0 || !(list.has_more ?? false)) break;
    const last = page[page.length - 1];
    cursor = { ts: last.created_at, id: last.id };
  }
  const [deltaBlocks, workspaceData] = await Promise.all([
    fetchBlocks(blockAfter || loadedLo.value),
    ctx.bus.request("chat:_:workspace:get", {}) as Promise<Workspace>,
  ]);
  chat.value = chats.find((item) => item.id === ctx.instanceId) ?? null;
  if (!chat.value) return false;
  const byId = new Map(messages.value.map((item) => [item.id, item] as const));
  for (const item of fetched) byId.set(item.id, item); // delta rows replace cached ones
  messages.value = [...byId.values()].sort((a, b) => a.created_at - b.created_at || a.id.localeCompare(b.id));
  if (loadedLo.value === 0 && messages.value.length > 0) {
    // First load through the cache: adopt the pagination boundary from the page.
    loadedLo.value = messages.value[0].created_at;
    olderCursor.value = { ts: messages.value[0].created_at, id: messages.value[0].id };
    hasOlder.value = firstHasMore;
  }
  const blockById = new Map(blocks.value.map((item) => [item.id, item] as const));
  // Merged streaming blocks mutate in place (text grows under the same id), so
  // delta rows must REPLACE cached ones, not dedupe-skip like immutable rows.
  for (const item of deltaBlocks) blockById.set(item.id, item);
  blocks.value = [...blockById.values()].sort((a, b) => a.occurred_at - b.occurred_at || a.id.localeCompare(b.id));
  roles.value = workspaceData.roles;
  workspace.value = workspaceData;
  setChrome();
  writeBack();
  enforceWindow("top");
  return true;
}

/** Merge-refresh entry point for activation / chat-list mutations: evict the
 *  cache and clear local state when the chat has vanished. A detached window
 *  reattaches through a full reload (activation implies the user is here). */
async function refresh(): Promise<void> {
  if (loadingInitial.value) return; // initial load() already fetches everything
  if (hasNewer.value) {
    await load(true); // detached: the delta cap may not reach the live edge
    return;
  }
  if (!(await refreshDelta())) {
    messages.value = [];
    blocks.value = [];
    hasOlder.value = false;
    olderCursor.value = null;
    removeEntry(ctx.instanceId);
  }
  streamingMessageId = "";
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
    const spanBlocks = await fetchBlocks(newLo, loadedLo.value);
    const known = new Set(messages.value.map((item) => item.id));
    messages.value = [...page.filter((item) => !known.has(item.id)), ...messages.value];
    for (const block of spanBlocks) {
      if (!blocks.value.some((item) => item.id === block.id)) blocks.value.push(block);
    }
    hasOlder.value = list.has_more ?? false;
    olderCursor.value = { ts: newLo, id: page[0].id };
    loadedLo.value = newLo;
    writeBack();
    await nextTick();
    const threadNow = threadRef.value;
    if (threadNow) scrollThreadTop(threadNow.scrollHeight - previousScrollHeight + previousScrollTop);
  } catch (cause) {
    error.value = errorText(cause);
  } finally {
    loadingOlder.value = false;
  }
  enforceWindow("bottom"); // deep reading sheds pages off the bottom edge
}

/** Load one newer page after the detached window's upper edge (reverse of
 *  loadOlder). When the backend reports nothing even newer, the window has
 *  caught up with the live edge: reattach (live frames merge again) and
 *  close the fetch/publish race with a delta refresh. */
async function loadNewer(): Promise<void> {
  if (loadingInitial.value || loadingNewer.value || !hasNewer.value) return;
  loadingNewer.value = true;
  try {
    const newest = messages.value[messages.value.length - 1];
    const cursor = loadedHi.value ?? (newest ? { ts: newest.created_at, id: newest.id } : null);
    const list = await (ctx.bus.request("chat:_:chats:list", {
      chat_id: ctx.instanceId, include_messages: true,
      ...(cursor ? { after: cursor.ts, after_id: cursor.id } : {}), limit: PAGE_SIZE,
    }) as Promise<ChatList>);
    seedRunningTurns(list);
    const page = (list.messages ?? []).filter((item) => !messages.value.some((known) => known.id === item.id));
    if (page.length > 0) {
      const spanLo = cursor?.ts ?? loadedLo.value;
      const gapBlocks = await fetchBlocks(spanLo);
      messages.value = [...messages.value, ...page].sort((a, b) => a.created_at - b.created_at || a.id.localeCompare(b.id));
      const blockById = new Map(blocks.value.map((item) => [item.id, item] as const));
      for (const block of gapBlocks) blockById.set(block.id, block);
      blocks.value = [...blockById.values()].sort((a, b) => a.occurred_at - b.occurred_at || a.id.localeCompare(b.id));
    }
    hasNewer.value = list.has_more ?? false;
    const top = messages.value[messages.value.length - 1];
    loadedHi.value = top ? { ts: top.created_at, id: top.id } : null;
    writeBack();
    if (!hasNewer.value) {
      // Reattached: fold in anything published during the catch-up fetch,
      // then land on the message end.
      await refreshDelta();
      scrollThreadToMessageEnd();
    }
  } catch (cause) {
    error.value = errorText(cause);
  } finally {
    loadingNewer.value = false;
  }
  enforceWindow("top");
}

/** Jump back to the live edge (bottom bar / send while detached): a fresh
 *  reload resets the window to the newest page (the cache-hydrate + bounded
 *  delta path cannot close an arbitrarily deep detach gap). */
function jumpToLatest(): void {
  void load(true).catch((cause) => { error.value = errorText(cause); });
}

function handleThreadScroll(): void {
  if (loadingInitial.value) return; // structural churn of a (re)load
  if (Date.now() - lastProgrammaticScrollAt < 400) return; // programmatic echo
  userScrolled = true;
  const thread = threadRef.value;
  if (!thread) return;
  const scrolledUp = thread.scrollTop < lastObservedScrollTop - 4;
  lastObservedScrollTop = thread.scrollTop;
  if (thread.scrollTop <= OLDER_SCROLL_THRESHOLD) void loadOlder();
  const scrollBottom = thread.scrollHeight - thread.scrollTop - thread.clientHeight;
  // Detach only on genuine upward scrolling far from the edge — content
  // growth under a stationary viewport (streaming) never detaches.
  if (!hasNewer.value && scrolledUp && scrollBottom > LIVE_DETACH_VIEWPORTS * thread.clientHeight) {
    hasNewer.value = true;
    const newest = messages.value[messages.value.length - 1];
    loadedHi.value = newest ? { ts: newest.created_at, id: newest.id } : null;
    return;
  }
  if (hasNewer.value && scrollBottom <= NEWER_SCROLL_THRESHOLD) void loadNewer();
}

function patchPendingSend(key: string, patch: Partial<PendingSend>): void {
  const index = pendingSends.value.findIndex((item) => item.key === key);
  if (index >= 0) pendingSends.value[index] = { ...pendingSends.value[index], ...patch };
}

function dismissSend(key: string): void {
  pendingSends.value = pendingSends.value.filter((item) => item.key !== key);
}

/** Per-role "RoleName → agent / provider / model" routing summary, shown on
 *  the user box once dispatch has assigned the message. Queued roles (waiting
 *  for an in-flight turn) are annotated with 排队中. */
function routedLabelFor(startedIds: string[], queuedIds: string[]): string {
  const label = (roleId: string, queued: boolean): string => {
    const role = roles.value.find((item) => item.id === roleId);
    const name = role?.name ?? roleId;
    const target = roleTargetLabel(roleId);
    const base = target ? `${name} → ${target}` : name;
    return queued ? `${base}（排队中）` : base;
  };
  return [...startedIds.map((id) => label(id, false)), ...queuedIds.map((id) => label(id, true))].join("  ·  ");
}

async function send(text: string, forceNewSession = false, parallel = false): Promise<void> {
  const message = text.trim();
  if (message === "") return;
  error.value = "";
  if (hasNewer.value) jumpToLatest(); // a send always targets the live edge
  // Optimistic user box with a sending marker; dispatch latency (routing,
  // agent spawn) no longer leaves the thread looking idle.
  const key = `send:${Date.now()}:${Math.random().toString(36).slice(2, 8)}`;
  pendingSends.value = [...pendingSends.value, { key, text: message, ts: Date.now(), sending: true, routed: "", failed: "" }];
  void nextTick(() => scrollThreadToMessageEnd());
  try {
    const payload: Record<string, unknown> = { chat_id: ctx.instanceId, message };
    if (selected.value.length > 0) payload.role_ids = selected.value;
    if (forceNewSession) payload.force_new_session = true;
    if (parallel) payload.parallel_dispatch = true;
    const result = await ctx.bus.request("chat:_:dispatch", payload) as { role_ids: string[]; started_role_ids?: string[]; queued_role_ids?: string[] };
    // Busy roles come back in queued_role_ids: their message is held in the
    // per-role queue and starts when the in-flight turn ends, so no
    // optimistic response box yet — the user box carries the 排队中 label.
    const started = result.started_role_ids ?? result.role_ids;
    const queued = result.queued_role_ids ?? [];
    patchPendingSend(key, { sending: false, routed: routedLabelFor(started, queued) });
    // Show one optimistic response box per dispatched role immediately; each
    // resolves when that turn's first live event arrives. The bus's
    // chat:_:turn started frame marks the turn running shortly after.
    const now = Date.now();
    const pending = new Map(pendingTurns.value);
    for (const roleId of started) {
      const role = roles.value.find((item) => item.id === roleId);
      pending.set(roleId, { key: `pending:${roleId}:${now}`, roleId, label: role?.name ?? "Agent", ts: now });
    }
    pendingTurns.value = pending;
    scrollOnNextUserMessage = true; // scroll the just-sent query into view when it lands
  } catch (cause) {
    patchPendingSend(key, { sending: false, failed: errorText(cause) });
  }
}

async function stop(roleId?: string, turnId?: string): Promise<void> {
  try {
    const payload: Record<string, unknown> = { chat_id: ctx.instanceId };
    if (roleId) payload.role_id = roleId;
    if (turnId) payload.turn_id = turnId;
    await ctx.bus.request("chat:_:stop", payload);
  } catch (cause) {
    error.value = errorText(cause);
  }
}

// Two-click stop (old Python-viewer parity): the running chip in a turn
// box's header arms a 10s confirm window on first click (chip switches to
// "Stop?"); a second click inside the window stops that turn, otherwise the
// chip reverts to running. Keyed by turn id (pending boxes by box key) so
// parallel turns confirm independently.
const STOP_CONFIRM_MS = 10_000;
const confirmingStops = ref(new Set<string>());
const confirmTimers = new Map<string, ReturnType<typeof setTimeout>>();

function stopKey(box: TimelineBox): string {
  return box.turnId !== "" ? box.turnId : box.key;
}

function clearConfirm(key: string): void {
  const timer = confirmTimers.get(key);
  if (timer !== undefined) {
    clearTimeout(timer);
    confirmTimers.delete(key);
  }
  if (confirmingStops.value.delete(key)) confirmingStops.value = new Set(confirmingStops.value);
}

function clickTurnStatus(box: TimelineBox): void {
  const key = stopKey(box);
  if (!confirmingStops.value.has(key)) {
    confirmingStops.value = new Set([...confirmingStops.value, key]);
    confirmTimers.set(key, setTimeout(() => clearConfirm(key), STOP_CONFIRM_MS));
    return;
  }
  clearConfirm(key);
  void stop(box.roleId, box.turnId || undefined);
}

onMounted(() => {
  const refreshNow = (): void => { void refresh().catch(() => undefined); };
  ctx.bus.subscribe(`chat:${ctx.instanceId}:message`, (frame) => {
    const value = frame.value as ChatMessage;
    // Detached window: frames beyond the upper edge wait for loadNewer.
    if (beyondWindowEdge(value.created_at, value.id)) return;
    pendingMessageUpserts.set(value.id, value);
    // The user's own send echo flushes immediately (snappy composer);
    // assistant streams ride the batch timer.
    if (value.role === "user") flushStream(); else scheduleStreamFlush();
  });
  ctx.bus.subscribe(`chat:${ctx.instanceId}:block`, (frame) => {
    const value = frame.value as ChatBlock;
    if (beyondWindowEdge(value.occurred_at, value.id)) return;
    pendingBlockUpserts.set(value.id, value);
    scheduleStreamFlush();
  });
  // Turn lifecycle feed: started/completed per turn, filtered to this chat.
  // A queued message starts its turn only when the in-flight turn ends, so
  // this feed (not the dispatch reply) is the authority on what is running;
  // it also drives the running chips of parallel turns of the same role.
  ctx.bus.subscribe("chat:_:turn", (frame) => {
    const value = frame.value as { chat_id: string; turn_id: string; role_id: string; phase: string };
    if (value.chat_id !== ctx.instanceId || !value.turn_id) return;
    if (value.phase === "started") {
      if (!runningTurns.value.has(value.turn_id)) {
        runningTurns.value = new Map([...runningTurns.value, [value.turn_id, { roleId: value.role_id }]]);
      }
      return;
    }
    if (value.phase !== "completed") return;
    if (runningTurns.value.delete(value.turn_id)) runningTurns.value = new Map(runningTurns.value);
    clearConfirm(value.turn_id);
    resolvePendingTurn(value.role_id);
    if (streamingMessageId !== "") {
      streamingMessageId = "";
      renderMermaidAtBoundary();
    }
  });
  ctx.bus.subscribe("chat:_:active", (frame) => {
    if (frame.value === ctx.instanceId) refreshNow();
  });
  window.addEventListener("viewer:chats-changed", refreshNow);
  ctx.onDispose(() => {
    window.removeEventListener("viewer:chats-changed", refreshNow);
    writeBack();
  });
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
            <span v-if="box.sending" class="chat-turn-status">
              <span class="spinner-border spinner-border-sm" aria-hidden="true" /> 发送中…
            </span>
            <template v-else-if="box.failed">
              <span class="chat-send-failed" :title="box.failed">发送失败</span>
              <button
                class="btn btn-sm btn-link chat-stop-turn"
                type="button"
                title="移除这条消息"
                aria-label="移除这条发送失败的消息"
                @click="dismissSend(box.key)"
              >
                <i class="bi bi-x-lg" />
              </button>
            </template>
            <span v-else-if="box.kind === 'user' && box.routed" class="chat-meta-detail">→ {{ box.routed }}</span>
            <button
              v-if="turnActive(box)"
              class="chat-turn-status chat-turn-chip"
              :class="{ confirming: confirmingStops.has(stopKey(box)) }"
              type="button"
              :title="confirmingStops.has(stopKey(box)) ? `10 秒内再次点击确认停止 ${box.label} 的当前回复` : '点击停止当前回复'"
              :aria-label="confirmingStops.has(stopKey(box)) ? `确认停止 ${box.label} 的当前回复` : `停止 ${box.label} 的当前回复`"
              @click="clickTurnStatus(box)"
            >
              <template v-if="confirmingStops.has(stopKey(box))">
                <i class="bi bi-question-circle" aria-hidden="true" /> Stop?
              </template>
              <template v-else>
                <span class="spinner-border spinner-border-sm" aria-hidden="true" /> running
              </template>
            </button>
            <span v-if="box.kind === 'role' && roleTargetLabel(box.roleId)" class="chat-meta-detail">{{ roleTargetLabel(box.roleId) }}</span>
            <span v-if="box.kind === 'role' && usageLabel(box)" class="chat-meta-detail" :title="usageTitle(box)">{{ usageLabel(box) }}</span>
            <span class="chat-time">{{ formatTime(box.ts) }}</span>
          </div>
        </div>
        <div class="chat-box-body chat-timeline">
          <div v-if="box.pending" class="chat-pending-shimmer" aria-hidden="true" />
          <template v-for="segment in box.segments" :key="segment.id">
            <div v-if="segment.kind === 'text' && box.kind === 'user'" class="chat-user-text">{{ segment.text }}</div>
            <div
              v-else-if="segment.kind === 'text' && (segment.text ?? '').trim()"
              class="markdown-content chat-response-body"
              v-html="renderedHtmlFor(segment.id, segment.text ?? '')"
            />
            <div v-else-if="segment.kind === 'activity'" class="chat-activity">
              <details v-if="activityHasBody(segment)" class="chat-activity-details">
                <summary class="chat-activity-summary" :class="{ 'text-danger': segment.block?.kind === 'error' }">
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
              <div v-else class="chat-activity-summary chat-activity-flat" :class="{ 'text-danger': segment.block?.kind === 'error' }">
                <i class="bi chat-activity-icon" :class="activityIcon(segment.block)" />
                <span class="chat-activity-label">{{ activityLabel(segment.block) }}</span>
                <span class="chat-activity-text">{{ activitySummary(segment.block) }}</span>
                <span class="chat-time">{{ formatTime(segment.ts) }}</span>
              </div>
            </div>
          </template>
        </div>
      </article>
      <div v-if="timeline.length" ref="messageEndRef" class="chat-thread-message-end" aria-hidden="true" />
      <div v-if="timeline.length && chatSettings.virtualSpace" class="chat-thread-virtual-space" aria-hidden="true" />
      <div v-if="!timeline.length" class="chat-empty">Write one message and dispatch it into this chat.</div>
    </div>
    <div v-if="hasNewer" class="chat-newer-bar">
      <button type="button" class="chat-newer-jump" @click="jumpToLatest">
        <span v-if="loadingNewer" class="spinner-border spinner-border-sm" aria-hidden="true" />
        <i v-else class="bi bi-arrow-down" aria-hidden="true" /> 新消息 — 跳到最新
      </button>
    </div>
    <div class="composer-shell">
      <div v-if="error" class="small text-danger mb-1">{{ error }}</div>
      <ComposerBox
        v-model:selected-role-ids="selected"
        :roles="members"
        :context-id="'chat:' + ctx.instanceId"
        @send="send"
      />
    </div>
  </section>
</template>

<style scoped>
.chat-pane {
  position: relative;
}

/* Size containment isolates the (potentially tens of thousands of) message
 * nodes from layout propagation: without it, every keystroke in the composer
 * triggered a full-document reflow (~200-250ms once many pages were loaded,
 * freezing the input). The thread size comes from flex (external), so strict
 * containment does not change its box or scroll metrics. */
.chat-thread {
  contain: strict;
}

/* Detached-window bar: shown between thread and composer while the user
   reads history above the live edge; click jumps back to the newest page. */
.chat-newer-bar {
  display: flex;
  justify-content: center;
  padding: 2px 0;
}

.chat-newer-jump {
  align-items: center;
  background: none;
  border: 0;
  color: var(--bs-primary);
  cursor: pointer;
  display: inline-flex;
  font-size: var(--font-size-ui);
  gap: 4px;
  padding: 0 6px;
}

.chat-newer-jump:hover {
  text-decoration: underline;
}

.chat-newer-jump .spinner-border {
  height: 10px;
  width: 10px;
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

/* Clickable running chip (two-click stop): looks like the plain status
   text, turns danger-colored while the 10s confirm window is armed. */
.chat-turn-chip {
  background: none;
  border: 0;
  cursor: pointer;
  padding: 0;
}

.chat-turn-chip:hover {
  color: var(--bs-primary-text-emphasis, var(--bs-primary));
}

.chat-turn-chip.confirming,
.chat-turn-chip.confirming:hover {
  color: var(--bs-danger);
}

.chat-send-failed {
  color: var(--bs-danger);
  font-size: 10.5px;
  line-height: 1.2;
}

.chat-stop-turn {
  color: var(--bs-danger);
  font-size: 12px;
  line-height: 1;
  padding: 0;
  text-decoration: none;
}

.chat-stop-turn:hover {
  color: var(--bs-danger-text-emphasis, var(--bs-danger));
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

/* Optimistic running placeholder: a low-contrast shimmer sweep inside the
   response box until the turn's first live event lands. */
.chat-pending-shimmer {
  animation: chat-pending-sweep 1.4s linear infinite;
  background: linear-gradient(
    90deg,
    color-mix(in srgb, var(--color-text-muted) 8%, transparent) 25%,
    color-mix(in srgb, var(--color-text-muted) 22%, transparent) 50%,
    color-mix(in srgb, var(--color-text-muted) 8%, transparent) 75%
  );
  background-size: 200% 100%;
  border-radius: var(--radius-sm);
  height: 14px;
}

@keyframes chat-pending-sweep {
  from { background-position: 200% 0; }
  to { background-position: -200% 0; }
}

@media (prefers-reduced-motion: reduce) {
  .chat-pending-shimmer {
    animation: none;
  }
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

.chat-thread-message-end {
  height: 0;
}

/* Old-viewer parity: one viewport of empty space after the final message, so
   the latest turn can be scrolled toward the middle/top of the pane. */
.chat-thread-virtual-space {
  height: calc(100% - 20px);
  min-height: 120px;
  pointer-events: none;
}

.chat-history-boundary {
  padding: 6px 0;
  text-align: center;
  user-select: none;
}

.composer-shell {
  flex: 0 0 auto;
  min-width: 0;
  padding: 0;
  width: 100%;
  z-index: 5;
}
</style>
