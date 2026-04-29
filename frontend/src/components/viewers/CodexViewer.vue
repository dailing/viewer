<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { codexSessionSocketUrl, getCodexSession } from "../../api/client";
import { useCodexStore } from "../../stores/codex";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import VoiceInputButton from "../VoiceInputButton.vue";
import type { PaneToolbarAction } from "../../stores/paneToolbar";
import type { CodexEvent, CodexSessionInfo, CodexSessionSnapshot } from "../../types/codex";

const props = defineProps<{ id: string; paneId: string }>();
const codex = useCodexStore();
const paneToolbar = usePaneToolbarStore();
const session = ref<CodexSessionSnapshot | null>(null);
const promptText = ref("");
const error = ref("");
const scroller = ref<HTMLElement | null>(null);
const showRaw = ref(false);

type CodexMessage =
  | { type: "snapshot"; session: CodexSessionSnapshot }
  | { type: "event"; event: CodexEvent; session: CodexSessionInfo }
  | { type: "status"; session: CodexSessionInfo }
  | { type: "deleted" };

let socket: WebSocket | null = null;
let reconnectTimer: number | null = null;
let mounted = false;

const canSend = computed(() => Boolean(promptText.value.trim()) && session.value?.status !== "running");
const sortedEvents = computed(() => [...(session.value?.events ?? [])].sort((a, b) => a.index - b.index));
type TranscriptEntry =
  | { kind: "prompt"; id: string; text: string }
  | { kind: "event"; id: string; event: CodexEvent; visible: boolean };
const transcriptEntries = computed<TranscriptEntry[]>(() => {
  const prompts = session.value?.prompts ?? [];
  const groups: CodexEvent[][] = [];
  let current: CodexEvent[] | null = null;

  for (const event of sortedEvents.value) {
    const type = rawType(event.raw);
    if (type === "turn.started") {
      current = [];
      groups.push(current);
      continue;
    }
    if (type === "thread.started") continue;
    if (!current) {
      current = [];
      groups.push(current);
    }
    current.push(event);
    if (type === "turn.completed") current = null;
  }

  const entries: TranscriptEntry[] = [];
  const count = Math.max(prompts.length, groups.length);
  for (let index = 0; index < count; index += 1) {
    const prompt = prompts[index];
    if (prompt) entries.push({ kind: "prompt", id: `prompt-${index}`, text: prompt.text });
    for (const event of groups[index] ?? []) {
      entries.push({ kind: "event", id: `event-${event.index}`, event, visible: Boolean(eventText(event)) || showRaw.value });
    }
  }
  return entries;
});

function rawType(raw: Record<string, unknown>): string {
  const item = raw.item;
  if (item && typeof item === "object" && "type" in item && typeof item.type === "string") return item.type;
  const direct = raw.type;
  if (typeof direct === "string") return direct;
  const nested = raw.msg;
  if (nested && typeof nested === "object" && "type" in nested && typeof nested.type === "string") return nested.type;
  return "event";
}

function textFrom(value: unknown, depth = 0): string {
  if (depth > 6 || value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (Array.isArray(value)) return value.map((item) => textFrom(item, depth + 1)).filter(Boolean).join("\n");
  if (typeof value !== "object") return "";

  const record = value as Record<string, unknown>;
  for (const key of ["message", "text", "content", "output", "summary", "final_answer", "item"]) {
    const found = textFrom(record[key], depth + 1);
    if (found) return found;
  }
  if (Array.isArray(record.changes)) {
    return record.changes
      .map((change) => {
        if (!change || typeof change !== "object") return "";
        const item = change as Record<string, unknown>;
        const kind = typeof item.kind === "string" ? item.kind : "change";
        const path = typeof item.path === "string" ? item.path : "";
        return path ? `${kind}: ${path}` : kind;
      })
      .filter(Boolean)
      .join("\n");
  }
  if (record.usage && typeof record.usage === "object") {
    const usage = record.usage as Record<string, unknown>;
    const input = typeof usage.input_tokens === "number" ? usage.input_tokens : undefined;
    const output = typeof usage.output_tokens === "number" ? usage.output_tokens : undefined;
    if (input !== undefined || output !== undefined) return `tokens: input ${input ?? "-"}, output ${output ?? "-"}`;
  }
  return "";
}

function eventText(event: CodexEvent): string {
  return textFrom(event.raw);
}

function shortJson(value: unknown): string {
  return JSON.stringify(value, null, 2);
}

function messageHtml(text: string): string {
  return renderMarkdown(text);
}

function applyInfo(info: CodexSessionInfo) {
  codex.upsert(info);
  if (!session.value) return;
  session.value.codex_session_id = info.codex_session_id;
  session.value.title = info.title;
  session.value.cwd = info.cwd;
  session.value.status = info.status;
  session.value.exit_code = info.exit_code;
  session.value.event_count = info.event_count;
  session.value.updated_at = info.updated_at;
}

function applySnapshot(snapshot: CodexSessionSnapshot) {
  session.value = snapshot;
  codex.upsert(snapshot);
  updatePaneToolbar();
  void renderRichContent();
  scrollToBottom();
}

function applyEvent(event: CodexEvent, info: CodexSessionInfo) {
  if (!session.value) return;
  if (!session.value.events.some((item) => item.index === event.index)) {
    session.value.events.push(event);
  }
  applyInfo(info);
  updatePaneToolbar();
  void renderRichContent();
  scrollToBottom();
}

function scrollToBottom() {
  void nextTick(() => {
    if (scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight;
  });
}

async function renderRichContent() {
  await renderMermaidIn(scroller.value, "codex-mermaid");
}

async function loadSnapshot() {
  try {
    error.value = "";
    applySnapshot(await getCodexSession(props.id));
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function connect() {
  if (reconnectTimer !== null) {
    window.clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  socket?.close();
  socket = new WebSocket(codexSessionSocketUrl(props.id));
  socket.addEventListener("message", (event) => {
    const message = JSON.parse(event.data) as CodexMessage;
    if (message.type === "snapshot") applySnapshot(message.session);
    if (message.type === "event") applyEvent(message.event, message.session);
    if (message.type === "status") applyInfo(message.session);
    if (message.type === "deleted") error.value = "Codex session was deleted.";
    updatePaneToolbar();
  });
  socket.addEventListener("close", () => {
    if (!mounted) return;
    if (!session.value) void loadSnapshot();
    reconnectTimer = window.setTimeout(connect, 1200);
  });
  socket.addEventListener("error", () => {
    error.value = "Codex session connection failed.";
  });
}

async function sendPrompt() {
  const prompt = promptText.value.trim();
  if (!prompt || !session.value) return;
  error.value = "";
  promptText.value = "";
  session.value.prompts.push({ text: prompt, created_at: Date.now() / 1000 });
  try {
    applyInfo(await codex.send(props.id, prompt));
    scrollToBottom();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function updatePaneToolbar() {
  const status = session.value?.status ?? "connecting";
  const actions: PaneToolbarAction[] = [
    {
      id: "codex-raw",
      title: showRaw.value ? "Hide raw Codex JSON" : "Show raw Codex JSON",
      icon: "bi-braces",
      active: showRaw.value,
      run: () => {
        showRaw.value = !showRaw.value;
        updatePaneToolbar();
      },
    },
  ];
  paneToolbar.setPaneToolbar(props.paneId, {
    title: session.value?.title ?? "Codex",
    status,
    statusClass: status,
    actions,
  });
}

watch(() => props.id, () => {
  session.value = null;
  void loadSnapshot();
  connect();
});
watch(showRaw, updatePaneToolbar);
watch(transcriptEntries, () => {
  void renderRichContent();
});

onMounted(() => {
  mounted = true;
  void loadSnapshot();
  connect();
  updatePaneToolbar();
});

onUnmounted(() => {
  mounted = false;
  if (reconnectTimer !== null) window.clearTimeout(reconnectTimer);
  socket?.close();
  paneToolbar.clearPaneToolbar(props.paneId);
});
</script>

<template>
  <div class="codex-viewer">
    <div ref="scroller" class="codex-scroll">
      <div v-if="error" class="codex-error">{{ error }}</div>

      <div v-if="session" class="session-meta">
        <span>{{ session.codex_session_id || "not started" }}</span>
        <span>{{ session.cwd }}</span>
      </div>

      <div v-if="session?.status === 'idle'" class="empty-codex">
        <i class="bi bi-stars"></i>
        <span>Send a message to start this Codex session.</span>
      </div>

      <template v-for="entry in transcriptEntries" :key="entry.id">
        <div v-if="entry.kind === 'prompt'" class="message user-message">
          <div class="message-label">You</div>
          <div class="message-text markdown-content" v-html="messageHtml(entry.text)"></div>
        </div>

        <div v-else-if="entry.visible" class="message event-message">
          <div class="message-label">{{ rawType(entry.event.raw) }}</div>
          <div v-if="eventText(entry.event)" class="message-text markdown-content" v-html="messageHtml(eventText(entry.event))"></div>
          <details v-if="showRaw" class="raw-event" open>
            <summary>JSON #{{ entry.event.index }}</summary>
            <pre>{{ shortJson(entry.event.raw) }}</pre>
          </details>
        </div>
      </template>

      <div v-if="session?.status === 'running'" class="running-row">
        <span class="spinner-border spinner-border-sm" role="status"></span>
        <span>Running</span>
      </div>
    </div>

    <form class="codex-input" @submit.prevent="sendPrompt">
      <textarea v-model="promptText" rows="3" placeholder="Send a message to this Codex session"></textarea>
      <VoiceInputButton v-model="promptText" />
      <button class="btn btn-primary" type="submit" :disabled="!canSend">
        <i class="bi bi-send-fill"></i>
      </button>
    </form>
  </div>
</template>

<style scoped>
.codex-viewer {
  background: #f7f8fb;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.codex-scroll {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
  padding: 12px;
}

.session-meta {
  color: var(--text-muted);
  display: flex;
  flex-wrap: wrap;
  font-size: 11px;
  gap: 8px;
  margin-bottom: 10px;
}

.message {
  border: 1px solid var(--border);
  border-radius: 8px;
  margin-bottom: 10px;
  overflow: hidden;
  padding: 8px 9px;
}

.user-message {
  background: #ffffff;
}

.event-message {
  background: #fbfcff;
}

.message-label {
  color: var(--text-muted);
  font-size: 10px;
  font-weight: 500;
  line-height: 1.2;
  margin-bottom: 5px;
  text-transform: lowercase;
}

.message-text {
  --markdown-render-body-size: 13px;
  --markdown-render-h1-size: 20px;
  --markdown-render-h2-size: 17px;
  --markdown-render-h3-size: 15px;
  --markdown-render-h4-size: 15px;
  --markdown-render-paragraph-line-height: 1.5;
  --markdown-render-paragraph-size: 13px;
  --markdown-render-pre-padding: 10px;
  font-family: inherit;
  font-size: 13px;
  line-height: 1.45;
  margin: 0;
  padding: 0;
  word-break: break-word;
}

.raw-event {
  border-top: 1px solid var(--border);
  font-size: 12px;
  margin-top: 8px;
  padding-top: 7px;
}

.raw-event pre {
  margin: 7px 0 0;
  overflow: auto;
  white-space: pre-wrap;
}

.codex-error {
  background: #fff1f1;
  border: 1px solid #efc7c7;
  border-radius: 8px;
  color: #a33;
  margin-bottom: 10px;
  padding: 9px;
}

.running-row {
  align-items: center;
  color: var(--text-muted);
  display: flex;
  gap: 8px;
  padding: 6px 2px;
}

.empty-codex {
  align-items: center;
  color: var(--text-muted);
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 150px;
  justify-content: center;
  text-align: center;
}

.empty-codex .bi {
  font-size: 28px;
}

.codex-input {
  align-items: stretch;
  background: #ffffff;
  border-top: 1px solid var(--border);
  display: flex;
  flex: 0 0 auto;
  gap: 8px;
  padding: 8px;
}

.codex-input textarea {
  border: 1px solid var(--border);
  border-radius: 8px;
  flex: 1 1 auto;
  font: inherit;
  min-width: 0;
  padding: 8px;
  resize: vertical;
}

.codex-input button {
  flex: 0 0 44px;
}

.codex-input :deep(.voice-input-button) {
  flex: 0 0 44px;
}
</style>
