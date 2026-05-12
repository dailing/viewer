<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { getHermesSession, hermesSessionSocketUrl, resolveDirectoryLink } from "../../api/client";
import { useHermesStore } from "../../stores/hermes";
import { useLayoutStore } from "../../stores/layout";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { useVoiceStore } from "../../stores/voice";
import { useWorkspacesStore } from "../../stores/workspaces";
import type { CodexEvent, CodexQueueItem } from "../../types/codex";
import type { HermesSessionInfo, HermesSessionSnapshot } from "../../types/hermes";
import { isLocalLinkTarget, renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import VoiceInputButton from "../VoiceInputButton.vue";
import LocalFilePreview from "../LocalFilePreview.vue";

const props = defineProps<{ id: string; paneId: string }>();

type HermesSocketMessage =
  | { type: "snapshot"; session: HermesSessionSnapshot; source?: string }
  | { type: "event"; event: CodexEvent; session: HermesSessionInfo; source?: string }
  | { type: "status"; session: HermesSessionInfo; source?: string }
  | { type: "deleted"; source?: string };

const hermes = useHermesStore();
const layout = useLayoutStore();
const paneToolbar = usePaneToolbarStore();
const voice = useVoiceStore();
const workspaces = useWorkspacesStore();
const session = ref<HermesSessionSnapshot | null>(null);
const error = ref("");
const promptText = ref("");
const stopping = ref(false);
const creatingSession = ref(false);
const rawJson = ref(false);
const scroller = ref<HTMLElement | null>(null);
const editingQueueItemId = ref<string | null>(null);
const previewPath = ref("");
const previewOpen = ref(false);
const socket = ref<WebSocket | null>(null);
const HERMES_DRAFTS_KEY = "viewer.hermesDrafts.v1";

const isActivePane = computed(() => layout.activePaneId === props.paneId);
const sortedEvents = computed(() => [...(session.value?.events ?? [])].sort((a, b) => a.index - b.index));
const editingQueueItem = computed(() => session.value?.queue.find((item) => item.id === editingQueueItemId.value) ?? null);
const isEditingQueue = computed(() => Boolean(editingQueueItem.value));
const voiceContextId = computed(() => `hermes:${props.id}:prompt`);

function readPromptDrafts(): Record<string, string> {
  try {
    const parsed = JSON.parse(localStorage.getItem(HERMES_DRAFTS_KEY) || "{}");
    return typeof parsed === "object" && parsed ? parsed : {};
  } catch {
    return {};
  }
}

function savePromptDraft(sessionId: string, text: string) {
  const drafts = readPromptDrafts();
  if (text) drafts[sessionId] = text;
  else delete drafts[sessionId];
  localStorage.setItem(HERMES_DRAFTS_KEY, JSON.stringify(drafts));
}

function clearPromptDraft(sessionId: string) {
  savePromptDraft(sessionId, "");
}

function eventClass(event: CodexEvent) {
  if (event.event_type.startsWith("message:assistant")) return "assistant";
  if (event.event_type.includes("tool")) return "tool";
  return "event";
}

function eventTitle(event: CodexEvent) {
  if (event.event_type === "message:assistant") return "Hermes";
  if (event.event_type === "tool" || event.event_type === "tool_call") return "Tool";
  return event.event_type;
}

function renderText(text: string) {
  return renderMarkdown(text, { baseDirectory: session.value?.cwd_relative ?? "" });
}

async function openHermesLink(event: MouseEvent) {
  const target = event.target as HTMLElement | null;
  const anchor = target?.closest<HTMLAnchorElement>("a[data-viewer-link='true']");
  if (!anchor || !session.value) return;
  const linkTarget = anchor.dataset.viewerTarget ?? anchor.getAttribute("href") ?? "";
  if (!isLocalLinkTarget(linkTarget)) return;
  event.preventDefault();
  try {
    const resolved = await resolveDirectoryLink(session.value.cwd_relative ?? "", linkTarget);
    previewPath.value = resolved.path;
    previewOpen.value = true;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function applyInfo(info: HermesSessionInfo) {
  hermes.upsert(info);
  if (!session.value) return;
  session.value.hermes_session_id = info.hermes_session_id;
  session.value.hermes_run_id = info.hermes_run_id;
  session.value.title = info.title;
  session.value.cwd = info.cwd;
  session.value.status = info.status;
  session.value.exit_code = info.exit_code;
  session.value.event_count = info.event_count;
  session.value.updated_at = info.updated_at;
  session.value.model = info.model;
  session.value.total_tokens = info.total_tokens;
  session.value.queue = info.queue ?? [];
  if (isActivePane.value) hermes.markRead(props.id);
  if (editingQueueItemId.value && !session.value.queue.some((item) => item.id === editingQueueItemId.value)) {
    editingQueueItemId.value = null;
  }
  updatePaneToolbar();
}

function applySnapshot(snapshot: HermesSessionSnapshot) {
  session.value = snapshot;
  hermes.upsert(snapshot);
  if (isActivePane.value) hermes.markRead(props.id);
  promptText.value = readPromptDrafts()[props.id] ?? "";
  void nextTick(() => renderMermaidIn(scroller.value, "hermes-mermaid"));
  updatePaneToolbar();
}

function applyEvent(event: CodexEvent, info: HermesSessionInfo) {
  if (!session.value) return;
  if (!session.value.events.some((item) => item.index === event.index)) {
    session.value.events.push(event);
  }
  applyInfo(info);
  if (isActivePane.value) hermes.markRead(props.id);
  else hermes.markUnread(props.id);
  void nextTick(() => renderMermaidIn(scroller.value, "hermes-mermaid"));
}

async function loadSnapshot() {
  error.value = "";
  try {
    applySnapshot(await getHermesSession(props.id));
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function connectSocket() {
  socket.value?.close();
  const activeSocket = new WebSocket(hermesSessionSocketUrl(props.id));
  socket.value = activeSocket;
  activeSocket.onmessage = (event) => {
    const message = JSON.parse(event.data) as HermesSocketMessage;
    if (message.type === "snapshot") applySnapshot(message.session);
    if (message.type === "event") applyEvent(message.event, message.session);
    if (message.type === "status") applyInfo(message.session);
    if (message.type === "deleted") error.value = "Hermes session was deleted.";
  };
  activeSocket.onerror = () => {
    if (!session.value) void loadSnapshot();
    error.value = "Hermes session connection failed.";
  };
}

async function queuePrompt() {
  const prompt = promptText.value.trim();
  if (!prompt || !session.value || isEditingQueue.value) return;
  try {
    applyInfo(await hermes.queue(props.id, prompt));
    promptText.value = "";
    clearPromptDraft(props.id);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function sendPrompt() {
  const prompt = promptText.value.trim();
  if (!prompt || !session.value || isEditingQueue.value) return;
  try {
    applyInfo(await hermes.send(props.id, prompt));
    promptText.value = "";
    clearPromptDraft(props.id);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function editQueuedMessage(item: CodexQueueItem) {
  editingQueueItemId.value = item.id;
  promptText.value = item.prompt;
}

async function saveQueuedMessage() {
  const prompt = promptText.value.trim();
  const itemId = editingQueueItemId.value;
  if (!prompt || !itemId || !session.value) return;
  try {
    applyInfo(await hermes.updateQueued(props.id, itemId, prompt));
    editingQueueItemId.value = null;
    promptText.value = "";
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function cancelQueuedEdit() {
  editingQueueItemId.value = null;
  promptText.value = readPromptDrafts()[props.id] ?? "";
}

async function deleteQueuedMessage(itemId: string) {
  try {
    applyInfo(await hermes.deleteQueued(props.id, itemId));
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function stopRun() {
  if (!session.value || session.value.status !== "running" || stopping.value) return;
  stopping.value = true;
  try {
    applyInfo(await hermes.terminate(props.id));
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    stopping.value = false;
  }
}

async function createSessionHere() {
  if (!session.value || creatingSession.value) return;
  creatingSession.value = true;
  try {
    const nextSession = await hermes.create("", session.value.cwd);
    workspaces.rememberActiveHermesSession(nextSession.id);
    layout.openHermesSession(nextSession.id);
  } finally {
    creatingSession.value = false;
  }
}

function handlePromptKeydown(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
    event.preventDefault();
    if (isEditingQueue.value) void saveQueuedMessage();
    else void sendPrompt();
  }
}

function updatePaneToolbar() {
  const status = session.value?.status ?? "connecting";
  const chips: string[] = [];
  if (session.value?.total_tokens) chips.push(`${session.value.total_tokens} tokens`);
  paneToolbar.setPaneToolbar(props.paneId, {
    title: session.value?.title ?? "Hermes",
    status: status === "running" ? "Hermes running" : status,
    statusClass: status === "failed" ? "status-danger" : status === "running" ? "status-running" : "",
    actions: [
      { id: "hermes-new-session-here", title: "New Hermes session in this directory", icon: "bi-plus-lg", run: () => createSessionHere() },
      { id: "hermes-refresh", title: "Refresh Hermes session", icon: "bi-arrow-clockwise", run: () => loadSnapshot() },
      { id: "hermes-raw", title: "Toggle raw JSON preview", label: "JSON", active: rawJson.value, run: () => { rawJson.value = !rawJson.value; updatePaneToolbar(); } },
    ],
    controls: chips.length ? [{ kind: "chips", id: "hermes-status", title: session.value?.db_path ?? undefined, items: chips }] : [],
  });
}

watch(promptText, (text) => {
  if (!isEditingQueue.value) savePromptDraft(props.id, text);
});

watch(() => props.id, () => {
  session.value = null;
  error.value = "";
  promptText.value = readPromptDrafts()[props.id] ?? "";
  connectSocket();
  void loadSnapshot();
});

watch(isActivePane, (active) => {
  if (active) hermes.markRead(props.id);
});

onMounted(() => {
  connectSocket();
  void loadSnapshot();
});

onUnmounted(() => {
  socket.value?.close();
  paneToolbar.clearPaneToolbar(props.paneId);
});
</script>

<template>
  <div class="hermes-viewer">
    <div ref="scroller" class="hermes-scroll" @click="openHermesLink">
      <div v-if="error" class="hermes-error">{{ error }}</div>
      <div v-if="session" class="session-meta">
        <span>{{ session.hermes_session_id || "not started" }}</span>
        <span>{{ session.cwd }}</span>
      </div>
      <div v-if="session?.status === 'idle'" class="empty-hermes">
        <i class="bi bi-lightning-charge"></i>
        <span>Send a message to start this Hermes session.</span>
      </div>
      <article v-for="event in sortedEvents" :key="event.index" class="event-card" :class="eventClass(event)">
        <header>
          <span>{{ eventTitle(event) }}</span>
          <span v-if="rawJson" class="event-type">{{ event.event_type }}</span>
        </header>
        <div v-if="event.text" class="markdown-body" v-html="renderText(event.text)"></div>
        <pre v-if="rawJson && event.raw_preview" class="raw-json">{{ JSON.stringify(event.raw_preview, null, 2) }}</pre>
      </article>
      <div v-if="session?.status === 'running'" class="running-row">
        <div class="spinner-border spinner-border-sm" role="status" aria-label="Hermes running"></div>
        <span>Hermes is running</span>
      </div>
    </div>

    <form class="hermes-input" @submit.prevent="isEditingQueue ? saveQueuedMessage() : sendPrompt()">
      <div v-if="session?.queue.length" class="hermes-queue">
        <div class="hermes-queue-title">Queue</div>
        <button
          v-for="(item, index) in session.queue"
          :key="item.id"
          class="hermes-queue-row"
          :class="{ active: item.id === editingQueueItemId }"
          type="button"
          @click="editQueuedMessage(item)"
        >
          <span class="hermes-queue-index">{{ index + 1 }}</span>
          <span class="hermes-queue-preview">{{ item.prompt.split(/\r?\n/)[0] }}</span>
          <button class="hermes-queue-delete" type="button" title="Delete queued message" @click.stop="deleteQueuedMessage(item.id)">
            <i class="bi bi-x"></i>
          </button>
        </button>
      </div>
      <div v-if="editingQueueItem" class="hermes-editing-row">
        Editing queued message
        <button type="button" class="btn btn-sm btn-link" @click="cancelQueuedEdit">Cancel</button>
      </div>
      <div class="hermes-input-box">
        <textarea v-model="promptText" rows="3" placeholder="Send a message to this Hermes session" @keydown="handlePromptKeydown"></textarea>
      </div>
      <div class="hermes-input-actions">
        <VoiceInputButton v-model="promptText" :context-id="voiceContextId" />
        <button class="btn btn-outline-secondary" type="button" :disabled="!promptText.trim() || isEditingQueue" @click="queuePrompt">Queue</button>
        <button class="btn btn-primary" type="submit" :disabled="!promptText.trim() || session?.status === 'running'">
          {{ isEditingQueue ? "Save" : "Send" }}
        </button>
        <button class="btn btn-outline-danger" type="button" :disabled="session?.status !== 'running' || stopping" @click="stopRun">Stop</button>
      </div>
    </form>
    <LocalFilePreview v-if="previewOpen" :path="previewPath" @close="previewOpen = false" />
  </div>
</template>

<style scoped>
.hermes-viewer {
  background: #f8fafc;
  color: #172033;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.hermes-scroll {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
  padding: 14px 18px 24px;
}

.session-meta {
  color: #667085;
  display: flex;
  flex-wrap: wrap;
  font-size: 12px;
  gap: 12px;
  margin-bottom: 12px;
}

.empty-hermes,
.running-row {
  align-items: center;
  color: #667085;
  display: flex;
  gap: 8px;
  justify-content: center;
  padding: 24px;
}

.event-card {
  border-left: 2px solid #cbd5e1;
  margin: 0 0 14px;
  padding: 0 0 0 12px;
}

.event-card.assistant {
  border-left-color: #2f6fdd;
}

.event-card.tool {
  border-left-color: #8a6d3b;
}

.event-card header {
  align-items: center;
  color: #475467;
  display: flex;
  font-size: 12px;
  font-weight: 700;
  gap: 8px;
  margin-bottom: 4px;
}

.event-type {
  color: #98a2b3;
  font-weight: 500;
}

.raw-json {
  background: #f1f5f9;
  border: 1px solid #d0d7de;
  border-radius: 6px;
  color: #344054;
  font-size: 12px;
  max-height: 260px;
  overflow: auto;
  padding: 8px;
}

.hermes-error {
  color: #a33;
  font-size: 13px;
  margin-bottom: 10px;
}

.hermes-input {
  background: #ffffff;
  border-top: 1px solid var(--border);
  flex: 0 0 auto;
  padding: 10px;
}

.hermes-queue {
  border: 1px solid #d0d7de;
  border-radius: 6px;
  margin-bottom: 8px;
  max-height: 150px;
  overflow: auto;
}

.hermes-queue-title {
  color: #667085;
  font-size: 11px;
  font-weight: 700;
  padding: 5px 8px;
  text-transform: uppercase;
}

.hermes-queue-row {
  align-items: center;
  background: transparent;
  border: 0;
  border-top: 1px solid #edf2f7;
  color: inherit;
  display: flex;
  gap: 8px;
  min-height: 28px;
  padding: 3px 8px;
  text-align: left;
  width: 100%;
}

.hermes-queue-row:hover,
.hermes-queue-row.active {
  background: #f1f5f9;
}

.hermes-queue-index {
  color: #667085;
  flex: 0 0 auto;
  font-size: 12px;
}

.hermes-queue-preview {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.hermes-queue-delete {
  background: transparent;
  border: 0;
  color: #667085;
  flex: 0 0 auto;
}

.hermes-editing-row {
  align-items: center;
  color: #667085;
  display: flex;
  font-size: 12px;
  gap: 6px;
  margin-bottom: 6px;
}

.hermes-input-box textarea {
  border: 1px solid #cbd5e1;
  border-radius: 6px;
  box-sizing: border-box;
  font: inherit;
  min-height: 72px;
  padding: 8px 10px;
  resize: vertical;
  width: 100%;
}

.hermes-input-actions {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  justify-content: flex-end;
  margin-top: 8px;
}
</style>
