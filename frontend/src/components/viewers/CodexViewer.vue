<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { codexSessionSocketUrl, getCodexSession } from "../../api/client";
import { useCodexStore } from "../../stores/codex";
import { useFilesStore } from "../../stores/files";
import { useLayoutStore } from "../../stores/layout";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { useVoiceStore } from "../../stores/voice";
import { renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import VoiceInputButton from "../VoiceInputButton.vue";
import type { PaneToolbarAction } from "../../stores/paneToolbar";
import type { CodexEvent, CodexQueueItem, CodexSessionInfo, CodexSessionSnapshot } from "../../types/codex";

const props = defineProps<{ id: string; paneId: string }>();
const codex = useCodexStore();
const files = useFilesStore();
const layout = useLayoutStore();
const paneToolbar = usePaneToolbarStore();
const voice = useVoiceStore();
const session = ref<CodexSessionSnapshot | null>(null);
const promptText = ref("");
const error = ref("");
const scroller = ref<HTMLElement | null>(null);
const focusMode = ref(false);
const stopping = ref(false);
const creatingSession = ref(false);
const editingQueueItemId = ref<string | null>(null);

type CodexMessage =
  | { type: "snapshot"; session: CodexSessionSnapshot }
  | { type: "event"; event: CodexEvent; session: CodexSessionInfo }
  | { type: "status"; session: CodexSessionInfo }
  | { type: "deleted" };

let socket: WebSocket | null = null;
let reconnectTimer: number | null = null;
let mounted = false;

const canSend = computed(() => Boolean(promptText.value.trim()) && session.value?.status !== "running");
const canQueue = computed(() => Boolean(promptText.value.trim()));
const isEditingQueue = computed(() => editingQueueItemId.value !== null);
const editingQueueItem = computed(() => session.value?.queue.find((item) => item.id === editingQueueItemId.value) ?? null);
const canClearPrompt = computed(() => Boolean(promptText.value));
const isActivePane = computed(() => layout.activePaneId === props.paneId);
const voiceContextId = computed(() => `codex:${props.id}:prompt`);
const codexViewerStyle = computed(() => {
  const alpha = clampAlpha(files.codexConfig.muted_message_alpha);
  const scaledAlpha = (scale: number) => Math.min(1, Math.max(0, alpha * scale)).toFixed(3);
  return {
    "--codex-muted-alpha": alpha.toFixed(3),
    "--codex-muted-text": `rgb(37 50 74 / ${scaledAlpha(0.72)})`,
    "--codex-muted-strong": `rgb(37 50 74 / ${scaledAlpha(0.86)})`,
    "--codex-muted-faint": `rgb(37 50 74 / ${scaledAlpha(0.5)})`,
    "--codex-muted-border": `rgb(118 134 160 / ${scaledAlpha(0.32)})`,
    "--codex-muted-surface": `rgb(255 255 255 / ${Math.min(0.82, 0.2 + alpha * 0.45).toFixed(3)})`,
  };
});
const sortedEvents = computed(() => [...(session.value?.events ?? [])].sort((a, b) => a.index - b.index));
const codexStatusItems = computed(() => {
  const status = session.value;
  if (!status) return [];
  const items: string[] = [];
  if (typeof status.context_used_percent === "number") items.push(`ctx ${status.context_used_percent}%`);
  if (typeof codex.status.primary_remaining_percent === "number" && codex.status.primary_window_minutes) {
    const hours = Math.round(codex.status.primary_window_minutes / 60);
    items.push(`${codex.status.primary_remaining_percent}%/${hours}h left`);
  }
  if (typeof codex.status.secondary_remaining_percent === "number" && codex.status.secondary_window_minutes) {
    const days = Math.round(codex.status.secondary_window_minutes / (60 * 24));
    items.push(`${codex.status.secondary_remaining_percent}%/${days}d left`);
  }
  return items;
});
type TranscriptEntry =
  | { kind: "prompt"; id: string; text: string }
  | { kind: "event"; id: string; event: CodexEvent; text: string; eventType: string; fileChanges: FileChange[]; patchText: string | null };
type TranscriptTimelineItem =
  | { kind: "prompt"; orderTime: number; orderIndex: number; entry: Extract<TranscriptEntry, { kind: "prompt" }> }
  | { kind: "event"; orderTime: number; orderIndex: number; entry: Extract<TranscriptEntry, { kind: "event" }> };
type FileChange = { path: string; changeType: string; diff: string | null };
type DiffLineKind = "add" | "delete" | "hunk" | "file" | "context";
type DiffSegment = { text: string; changed: boolean };
type DiffLine = { text: string; kind: DiffLineKind; segments: DiffSegment[] };
const transcriptEntries = computed<TranscriptEntry[]>(() => {
  const prompts = session.value?.prompts ?? [];
  const timeline: TranscriptTimelineItem[] = prompts.map((prompt, index) => ({
    kind: "prompt",
    orderTime: finiteTime(prompt.created_at),
    orderIndex: index * 2,
    entry: { kind: "prompt", id: `prompt-${index}`, text: prompt.text },
  }));

  for (const event of sortedEvents.value) {
    const text = event.text ?? "";
    const eventType = event.event_type;
    const muted = isMutedEventType(eventType);
    if (focusMode.value && muted) continue;
    const fileChanges = event.file_changes.map((change) => ({
      path: change.path,
      changeType: change.change_type,
      diff: change.diff ?? null,
    }));
    const patchText = event.patch_text ?? null;
    const visibleFileChanges = focusMode.value ? [] : fileChanges;
    const visiblePatchText = focusMode.value ? null : patchText;
    if (text || visibleFileChanges.length || visiblePatchText) {
      timeline.push({
        kind: "event",
        orderTime: finiteTime(event.received_at),
        orderIndex: event.index * 2 + 1,
        entry: { kind: "event", id: `event-${event.index}`, event, text, eventType, fileChanges: visibleFileChanges, patchText: visiblePatchText },
      });
    }
  }
  return timeline.sort(compareTranscriptTimelineItems).map((item) => item.entry);
});

function finiteTime(value: number): number {
  return Number.isFinite(value) ? value : 0;
}

function compareTranscriptTimelineItems(left: TranscriptTimelineItem, right: TranscriptTimelineItem): number {
  const timeDelta = left.orderTime - right.orderTime;
  if (timeDelta !== 0) return timeDelta;
  if (left.kind !== right.kind) return left.kind === "prompt" ? -1 : 1;
  return left.orderIndex - right.orderIndex;
}

function clampAlpha(value: unknown): number {
  const alpha = Number(value ?? 0.56);
  if (!Number.isFinite(alpha)) return 0.56;
  return Math.min(1, Math.max(0.15, alpha));
}

function isMutedEvent(entry: Extract<TranscriptEntry, { kind: "event" }>): boolean {
  return isMutedEventType(entry.eventType);
}

function isMutedEventType(eventType: string): boolean {
  if (eventType === "message:assistant" || eventType === "agent_message") return false;
  if (eventType.startsWith("tool:")) return true;
  return [
    "custom_tool_call",
    "exec_command_begin",
    "exec_command_end",
    "function_call",
    "patch_apply_end",
    "view_image_tool_call",
  ].includes(eventType);
}

function diffLines(text: string): DiffLine[] {
  const lines = text.split("\n").map((line) => {
    const marker = line[0] === " " && (line[1] === "+" || line[1] === "-") ? line[1] : line[0];
    const kind: DiffLineKind = line.startsWith("@@")
      ? "hunk"
      : line.startsWith("+++") || line.startsWith("---") || line.startsWith("***")
        ? "file"
        : marker === "+"
          ? "add"
          : marker === "-"
            ? "delete"
            : "context";
    return { text: line, kind, segments: [{ text: line || " ", changed: false }] };
  });
  return withWordDiffs(lines);
}

function cleanDiffText(text: string): string {
  const lines = text.split("\n");
  const result = lines
    .map((line) => {
      if (!line || line.startsWith("@@") || line.startsWith("+++") || line.startsWith("---") || line.startsWith("***")) return null;
      if (line.startsWith("\\ No newline")) return null;
      if (line[0] === " " && (line[1] === "+" || line[1] === "-")) {
        if (line[1] === "-") return null;
        return line.slice(2);
      }
      if (line[0] === "-") return null;
      if (line[0] === "+") return line.slice(1);
      if (line[0] === " ") return line.slice(1);
      return line;
    })
    .filter((line): line is string => line !== null);
  return trimBlankEdges(result).join("\n");
}

function trimBlankEdges(lines: string[]): string[] {
  let start = 0;
  let end = lines.length;
  while (start < end && !lines[start].trim()) start += 1;
  while (end > start && !lines[end - 1].trim()) end -= 1;
  return lines.slice(start, end);
}

function withWordDiffs(lines: DiffLine[]): DiffLine[] {
  const next = lines.map((line) => ({ ...line, segments: [...line.segments] }));
  for (let index = 0; index < next.length; index += 1) {
    if (next[index].kind !== "delete") continue;
    const deleteStart = index;
    while (index < next.length && next[index].kind === "delete") index += 1;
    const deleteLines = next.slice(deleteStart, index);
    if (next[index]?.kind !== "add") {
      index -= 1;
      continue;
    }
    const addStart = index;
    while (index < next.length && next[index].kind === "add") index += 1;
    const addLines = next.slice(addStart, index);
    const pairCount = Math.min(deleteLines.length, addLines.length);
    for (let pairIndex = 0; pairIndex < pairCount; pairIndex += 1) {
      const [deleteSegments, addSegments] = wordDiffSegments(deleteLines[pairIndex].text, addLines[pairIndex].text);
      deleteLines[pairIndex].segments = deleteSegments;
      addLines[pairIndex].segments = addSegments;
    }
    for (let deleteIndex = pairCount; deleteIndex < deleteLines.length; deleteIndex += 1) {
      deleteLines[deleteIndex].segments = [{ text: deleteLines[deleteIndex].text || " ", changed: true }];
    }
    for (let addIndex = pairCount; addIndex < addLines.length; addIndex += 1) {
      addLines[addIndex].segments = [{ text: addLines[addIndex].text || " ", changed: true }];
    }
    index -= 1;
  }
  return next;
}

function wordDiffSegments(deleteLine: string, addLine: string): [DiffSegment[], DiffSegment[]] {
  const deleteParts = splitDiffLine(deleteLine);
  const addParts = splitDiffLine(addLine);
  const deleteBody = tokenizeDiffBody(deleteParts.body);
  const addBody = tokenizeDiffBody(addParts.body);
  const matches = lcsMatches(deleteBody, addBody);
  const deleteMatched = new Set(matches.map(([deleteIndex]) => deleteIndex));
  const addMatched = new Set(matches.map(([, addIndex]) => addIndex));
  return [
    mergeSegments([{ text: deleteParts.prefix, changed: false }, ...deleteBody.map((token, index) => ({ text: token, changed: !deleteMatched.has(index) }))]),
    mergeSegments([{ text: addParts.prefix, changed: false }, ...addBody.map((token, index) => ({ text: token, changed: !addMatched.has(index) }))]),
  ];
}

function splitDiffLine(line: string): { prefix: string; body: string } {
  if (line[0] === " " && (line[1] === "+" || line[1] === "-")) return { prefix: line.slice(0, 2), body: line.slice(2) };
  if (line[0] === "+" || line[0] === "-") return { prefix: line[0], body: line.slice(1) };
  return { prefix: "", body: line };
}

function tokenizeDiffBody(text: string): string[] {
  return text.match(/(\s+|[A-Za-z0-9_]+|[^\sA-Za-z0-9_]+)/g) ?? [];
}

function lcsMatches(left: string[], right: string[]): [number, number][] {
  const table = Array.from({ length: left.length + 1 }, () => Array(right.length + 1).fill(0) as number[]);
  for (let leftIndex = left.length - 1; leftIndex >= 0; leftIndex -= 1) {
    for (let rightIndex = right.length - 1; rightIndex >= 0; rightIndex -= 1) {
      table[leftIndex][rightIndex] =
        left[leftIndex] === right[rightIndex]
          ? table[leftIndex + 1][rightIndex + 1] + 1
          : Math.max(table[leftIndex + 1][rightIndex], table[leftIndex][rightIndex + 1]);
    }
  }

  const matches: [number, number][] = [];
  let leftIndex = 0;
  let rightIndex = 0;
  while (leftIndex < left.length && rightIndex < right.length) {
    if (left[leftIndex] === right[rightIndex]) {
      matches.push([leftIndex, rightIndex]);
      leftIndex += 1;
      rightIndex += 1;
    } else if (table[leftIndex + 1][rightIndex] >= table[leftIndex][rightIndex + 1]) {
      leftIndex += 1;
    } else {
      rightIndex += 1;
    }
  }
  return matches;
}

function mergeSegments(segments: DiffSegment[]): DiffSegment[] {
  const merged: DiffSegment[] = [];
  for (const segment of segments) {
    if (!segment.text) continue;
    const previous = merged[merged.length - 1];
    if (previous && previous.changed === segment.changed) {
      previous.text += segment.text;
    } else {
      merged.push({ ...segment });
    }
  }
  return merged.length ? merged : [{ text: " ", changed: false }];
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
  session.value.model = info.model;
  session.value.model_context_window = info.model_context_window;
  session.value.context_used_percent = info.context_used_percent;
  session.value.total_tokens = info.total_tokens;
  session.value.queue = info.queue ?? [];
  if (editingQueueItemId.value && !session.value.queue.some((item) => item.id === editingQueueItemId.value)) {
    editingQueueItemId.value = null;
    promptText.value = "";
  }
}

function applySnapshot(snapshot: CodexSessionSnapshot) {
  session.value = snapshot;
  if (editingQueueItemId.value && !snapshot.queue.some((item) => item.id === editingQueueItemId.value)) {
    editingQueueItemId.value = null;
    promptText.value = "";
  }
  codex.upsert(snapshot);
  updatePaneToolbar();
  void renderRichContent();
  scrollToBottom(true);
}

function applyEvent(event: CodexEvent, info: CodexSessionInfo) {
  if (!session.value) return;
  if (!session.value.events.some((item) => item.index === event.index)) {
    session.value.events.push(event);
  }
  applyInfo(info);
  updatePaneToolbar();
  void renderRichContent();
  scrollToBottom(false);
  if (isActivePane.value) {
    codex.markRead(props.id);
  } else {
    codex.markUnread(props.id);
  }
}

function shouldAutoScroll(): boolean {
  if (!scroller.value) return true;
  const remaining = scroller.value.scrollHeight - scroller.value.scrollTop - scroller.value.clientHeight;
  return remaining < 60;
}

function scrollToBottom(force: boolean) {
  if (!force && !shouldAutoScroll()) return;
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
  if (socket) {
    const previous = socket;
    socket = null;
    previous.close();
  }
  const activeSocket = new WebSocket(codexSessionSocketUrl(props.id));
  socket = activeSocket;
  activeSocket.addEventListener("message", (event) => {
    if (socket !== activeSocket) return;
    const message = JSON.parse(event.data) as CodexMessage;
    if (message.type === "snapshot") applySnapshot(message.session);
    if (message.type === "event") applyEvent(message.event, message.session);
    if (message.type === "status") applyInfo(message.session);
    if (message.type === "deleted") error.value = "Codex session was deleted.";
    updatePaneToolbar();
  });
  activeSocket.addEventListener("close", () => {
    if (socket !== activeSocket) return;
    socket = null;
    if (!mounted) return;
    if (!session.value) void loadSnapshot();
    reconnectTimer = window.setTimeout(connect, 1200);
  });
  activeSocket.addEventListener("error", () => {
    if (socket !== activeSocket) return;
    error.value = "Codex session connection failed.";
    activeSocket.close();
  });
}

async function sendPrompt() {
  const prompt = promptText.value.trim();
  if (!prompt || !session.value || session.value.status === "running" || isEditingQueue.value) return;
  error.value = "";
  promptText.value = "";
  voice.clear(voiceContextId.value);
  session.value.prompts.push({ text: prompt, created_at: Date.now() / 1000 });
  try {
    applyInfo(await codex.send(props.id, prompt));
    scrollToBottom(true);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function queuePrompt() {
  const prompt = promptText.value.trim();
  if (!prompt || !session.value || isEditingQueue.value) return;
  error.value = "";
  promptText.value = "";
  voice.clear(voiceContextId.value);
  try {
    applyInfo(await codex.queue(props.id, prompt));
    scrollToBottom(true);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function editQueuedMessage(item: CodexQueueItem) {
  editingQueueItemId.value = item.id;
  promptText.value = item.prompt;
  void nextTick(() => {
    const textarea = document.querySelector<HTMLTextAreaElement>(".codex-input textarea");
    textarea?.focus();
  });
}

async function saveQueuedMessage() {
  const prompt = promptText.value.trim();
  const itemId = editingQueueItemId.value;
  if (!prompt || !itemId || !session.value) return;
  error.value = "";
  try {
    applyInfo(await codex.updateQueued(props.id, itemId, prompt));
    editingQueueItemId.value = null;
    promptText.value = "";
    voice.clear(voiceContextId.value);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function cancelQueuedEdit() {
  editingQueueItemId.value = null;
  promptText.value = "";
  voice.clear(voiceContextId.value);
}

async function deleteQueuedMessage(itemId: string) {
  if (!session.value) return;
  error.value = "";
  try {
    applyInfo(await codex.deleteQueued(props.id, itemId));
    if (editingQueueItemId.value === itemId) {
      editingQueueItemId.value = null;
      promptText.value = "";
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function clearPrompt() {
  promptText.value = "";
  voice.clear(voiceContextId.value);
}

async function stopRun() {
  if (!session.value || session.value.status !== "running" || stopping.value) return;
  stopping.value = true;
  error.value = "";
  try {
    applyInfo(await codex.terminate(props.id));
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    stopping.value = false;
  }
}

async function createSessionHere() {
  if (!session.value || creatingSession.value) return;
  creatingSession.value = true;
  error.value = "";
  try {
    const nextSession = await codex.create("", session.value.cwd);
    layout.openCodexSession(nextSession.id);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    creatingSession.value = false;
  }
}

function updatePaneToolbar() {
  const status = session.value?.status ?? "connecting";
  const actions: PaneToolbarAction[] = [
    {
      id: "codex-new-session-here",
      title: "New Codex session in this directory",
      icon: "bi-plus-square",
      run: () => createSessionHere(),
    },
    {
      id: "codex-refresh",
      title: "Refresh Codex messages",
      icon: "bi-arrow-clockwise",
      run: () => loadSnapshot(),
    },
    {
      id: "codex-focus",
      title: focusMode.value ? "Show Codex operation details" : "Focus mode: hide Codex operation details",
      icon: focusMode.value ? "bi-eye-slash" : "bi-eye",
      active: focusMode.value,
      run: () => {
        focusMode.value = !focusMode.value;
        updatePaneToolbar();
      },
    },
  ];
  if (session.value?.status === "running") {
    actions.push({
      id: "codex-stop",
      title: "Stop current run",
      icon: "bi-stop-fill",
      variant: "danger",
      run: () => stopRun(),
    });
  }
  paneToolbar.setPaneToolbar(props.paneId, {
    title: session.value?.title ?? "Codex",
    status,
    statusClass: status,
    actions,
    controls: [
      {
        kind: "select",
        id: "codex-model",
        title: "Model for new Codex turns",
        value: codex.models.selected_model,
        options: codex.models.available_models,
        size: "compact",
        onChange: (value) => codex.setSelectedModel(value),
      },
      {
        kind: "chips",
        id: "codex-status",
        title: session.value?.rollout_path ?? undefined,
        items: codexStatusItems.value,
      },
    ],
  });
}

watch(() => props.id, () => {
  socket?.close();
  session.value = null;
  void loadSnapshot();
});
watch(focusMode, updatePaneToolbar);
watch(() => [codex.models.selected_model, codex.models.available_models, codex.status], updatePaneToolbar, { deep: true });
watch(transcriptEntries, () => {
  void renderRichContent();
});
watch(isActivePane, (active) => {
  if (active) codex.markRead(props.id);
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
  <div class="codex-viewer" :style="codexViewerStyle">
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
          <div class="message-text markdown-content" v-html="messageHtml(entry.text)"></div>
        </div>

        <div v-else class="message event-message" :class="{ 'event-message-muted': isMutedEvent(entry) }">
          <div v-if="entry.text" class="message-text markdown-content" v-html="messageHtml(entry.text)"></div>
          <div v-if="entry.fileChanges.length" class="file-changes">
            <div class="file-change" v-for="change in entry.fileChanges" :key="`${entry.id}:${change.path}`">
              <div class="file-change-path">{{ change.changeType }} {{ change.path }}</div>
              <pre v-if="change.diff" class="file-change-diff"><span v-for="(line, index) in diffLines(change.diff)" :key="index" class="diff-line" :class="`diff-${line.kind}`"><span v-for="(segment, segmentIndex) in line.segments" :key="segmentIndex" :class="{ 'diff-word-change': segment.changed }">{{ segment.text }}</span></span></pre>
              <div v-if="change.diff && cleanDiffText(change.diff)" class="file-change-result">
                <div class="file-change-result-title">Result</div>
                <pre>{{ cleanDiffText(change.diff) }}</pre>
              </div>
            </div>
          </div>
          <div v-if="entry.patchText" class="patch-input">
            <div class="patch-input-title">Patch input</div>
            <pre class="file-change-diff"><span v-for="(line, index) in diffLines(entry.patchText)" :key="index" class="diff-line" :class="`diff-${line.kind}`"><span v-for="(segment, segmentIndex) in line.segments" :key="segmentIndex" :class="{ 'diff-word-change': segment.changed }">{{ segment.text }}</span></span></pre>
            <div v-if="cleanDiffText(entry.patchText)" class="file-change-result">
              <div class="file-change-result-title">Result</div>
              <pre>{{ cleanDiffText(entry.patchText) }}</pre>
            </div>
          </div>
        </div>
      </template>

      <div v-if="session?.status === 'running'" class="running-row">
        <span class="spinner-border spinner-border-sm" role="status"></span>
        <span>Running</span>
      </div>
    </div>

    <form class="codex-input" @submit.prevent="isEditingQueue ? saveQueuedMessage() : sendPrompt()">
      <div v-if="session?.queue.length" class="codex-queue">
        <div class="codex-queue-title">Queue</div>
        <div
          v-for="(item, index) in session.queue"
          :key="item.id"
          class="codex-queue-row"
          :class="{ active: item.id === editingQueueItemId }"
          role="button"
          tabindex="0"
          @click="editQueuedMessage(item)"
          @keydown.enter.prevent="editQueuedMessage(item)"
        >
          <span class="codex-queue-index">{{ index + 1 }}</span>
          <span class="codex-queue-preview">{{ item.prompt.split(/\r?\n/)[0] }}</span>
          <button class="codex-queue-delete" type="button" title="Delete queued message" @click.stop="deleteQueuedMessage(item.id)">
            <i class="bi bi-trash"></i>
          </button>
        </div>
      </div>
      <div v-if="editingQueueItem" class="codex-editing-row">
        Editing queued message
      </div>
      <div class="codex-input-box">
        <textarea v-model="promptText" rows="3" placeholder="Send a message to this Codex session"></textarea>
        <VoiceInputButton v-model="promptText" :context-id="voiceContextId" />
      </div>
      <div class="codex-input-actions">
        <template v-if="isEditingQueue">
          <button class="btn btn-primary" type="submit" :disabled="!promptText.trim()">
            <i class="bi bi-check2"></i>
            <span>Save</span>
          </button>
          <button class="btn btn-outline-secondary" type="button" @click="cancelQueuedEdit">
            <i class="bi bi-x-lg"></i>
            <span>Cancel</span>
          </button>
          <button class="btn btn-outline-danger" type="button" :disabled="!editingQueueItemId" @click="editingQueueItemId && deleteQueuedMessage(editingQueueItemId)">
            <i class="bi bi-trash"></i>
            <span>Delete</span>
          </button>
        </template>
        <template v-else>
        <button v-if="session?.status !== 'running'" class="btn btn-primary" type="submit" :disabled="!canSend">
          <i class="bi bi-send-fill"></i>
          <span>Send</span>
        </button>
        <button class="btn btn-outline-primary" type="button" :disabled="!canQueue" @click="queuePrompt">
          <i class="bi bi-list-ol"></i>
          <span>Queue</span>
        </button>
        <button class="btn btn-outline-secondary" type="button" :disabled="!canClearPrompt" @click="clearPrompt">
          <i class="bi bi-eraser"></i>
          <span>Clear</span>
        </button>
        <button class="btn btn-outline-danger" type="button" :disabled="session?.status !== 'running' || stopping" @click="stopRun">
          <i class="bi bi-stop-fill"></i>
          <span>{{ stopping ? "Stopping" : "Stop" }}</span>
        </button>
        </template>
      </div>
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
  user-select: text;
}

.codex-scroll {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
  padding: 12px;
  user-select: text;
}

.session-meta {
  color: var(--codex-muted-faint);
  display: flex;
  flex-wrap: wrap;
  font-size: 10px;
  gap: 8px;
  margin-bottom: 8px;
}

.message {
  margin-bottom: 8px;
  padding: 2px 0;
}

.user-message {
  background: #edf5ff;
  border-left: 3px solid #1f6feb;
  border-radius: 4px;
  padding: 6px 10px;
}

.event-message {
  background: transparent;
}

.event-message-muted {
  color: var(--codex-muted-text);
  margin-bottom: 3px;
  padding: 0;
}

.event-message-muted .message-text {
  --markdown-render-body-size: 11px;
  --markdown-render-h1-size: 14px;
  --markdown-render-h2-size: 13px;
  --markdown-render-h3-size: 12px;
  --markdown-render-h4-size: 12px;
  --markdown-render-paragraph-line-height: 1.28;
  --markdown-render-paragraph-size: 11px;
  --markdown-render-pre-padding: 4px;
  --markdown-body-color: var(--codex-muted-text);
  --markdown-border-color: var(--codex-muted-border);
  --markdown-code-background: transparent;
  --markdown-code-color: var(--codex-muted-strong);
  --markdown-h1-color: var(--codex-muted-strong);
  --markdown-h2-color: var(--codex-muted-strong);
  --markdown-h3-color: var(--codex-muted-strong);
  --markdown-h4-color: var(--codex-muted-strong);
  --markdown-link-color: var(--codex-muted-strong);
  --markdown-paragraph-color: var(--codex-muted-text);
  --syntax-background: var(--codex-muted-surface);
  --syntax-text: var(--codex-muted-text);
  color: var(--codex-muted-text);
  font-size: 11px;
  line-height: 1.28;
}

.event-message-muted .message-text :deep(p),
.event-message-muted .message-text :deep(li),
.event-message-muted .message-text :deep(pre),
.event-message-muted .message-text :deep(code) {
  color: var(--codex-muted-text);
}

.file-changes {
  color: var(--codex-muted-text);
  font-size: 10px;
  margin-top: 2px;
}

.file-change {
  border-left: 2px solid var(--codex-muted-border);
  margin-top: 2px;
  padding-left: 5px;
}

.file-change-path {
  color: var(--codex-muted-text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10px;
}

.file-change-diff {
  background: var(--codex-muted-surface);
  border: 1px solid var(--codex-muted-border);
  border-radius: 6px;
  color: var(--codex-muted-text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10px;
  line-height: 1.24;
  margin: 2px 0 0;
  overflow-wrap: anywhere;
  padding: 3px 0;
  white-space: pre-wrap;
}

.patch-input {
  color: var(--codex-muted-text);
  font-size: 10px;
  margin-top: 3px;
}

.patch-input-title {
  color: var(--codex-muted-text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10px;
  margin-bottom: 2px;
}

.file-change-result {
  margin-top: 2px;
}

.file-change-result-title {
  color: var(--codex-muted-text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10px;
  margin-bottom: 2px;
}

.file-change-result pre {
  background: var(--codex-muted-surface);
  border: 1px solid var(--codex-muted-border);
  border-radius: 6px;
  color: var(--codex-muted-text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10px;
  line-height: 1.24;
  margin: 0;
  overflow-wrap: anywhere;
  padding: 3px 5px;
  white-space: pre-wrap;
}

.diff-line {
  display: block;
  min-height: 1.24em;
  padding: 0 5px;
  user-select: text;
}

.diff-add {
  background: #f0f8f2;
  color: #4f8a61;
}

.diff-delete {
  background: #fff1f0;
  color: #c46a63;
}

.diff-hunk {
  background: #f3f7fc;
  color: #7791ba;
}

.diff-file {
  color: var(--codex-muted-text);
  font-weight: 500;
}

.diff-add .diff-word-change {
  background: #d9efdd;
}

.diff-delete .diff-word-change {
  background: #ffdeda;
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
  color: var(--codex-muted-faint);
  display: flex;
  font-size: 11px;
  gap: 6px;
  padding: 3px 2px;
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
  background: #ffffff;
  border-top: 1px solid var(--border);
  display: grid;
  flex: 0 0 auto;
  gap: 7px;
  padding: 8px;
}

.codex-queue {
  border: 1px solid #dde5f1;
  border-radius: 6px;
  display: grid;
  gap: 2px;
  max-height: 150px;
  overflow: auto;
  padding: 5px;
}

.codex-queue-title {
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 600;
  padding: 2px 4px 4px;
  text-transform: uppercase;
}

.codex-queue-row {
  align-items: center;
  border-radius: 4px;
  cursor: pointer;
  display: grid;
  gap: 6px;
  grid-template-columns: 22px minmax(0, 1fr) 28px;
  min-height: 30px;
  padding: 2px 2px 2px 4px;
}

.codex-queue-row:hover,
.codex-queue-row.active {
  background: #edf5ff;
}

.codex-queue-index {
  color: var(--text-muted);
  font-size: 11px;
  text-align: right;
}

.codex-queue-preview {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.codex-queue-delete {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: 4px;
  color: #8a94a6;
  display: inline-flex;
  height: 26px;
  justify-content: center;
  padding: 0;
  width: 26px;
}

.codex-queue-delete:hover {
  background: #ffe8e8;
  color: #b42318;
}

.codex-editing-row {
  color: #3b4d68;
  font-size: 12px;
}

.codex-input-box {
  min-width: 0;
  position: relative;
}

.codex-input textarea {
  border: 1px solid var(--border);
  border-radius: 6px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 14px;
  line-height: 1.35;
  min-height: 92px;
  outline: none;
  padding: 8px 46px 8px 8px;
  resize: vertical;
  width: 100%;
}

.codex-input textarea:focus {
  border-color: #1f6feb;
  box-shadow: 0 0 0 2px rgb(31 111 235 / 0.16);
}

.codex-input-box :deep(.voice-input-button) {
  align-items: center;
  border-radius: 999px;
  bottom: 8px;
  display: inline-flex;
  height: 34px;
  justify-content: center;
  padding: 0;
  position: absolute;
  right: 8px;
  width: 34px;
}

.codex-input-actions {
  display: flex;
  gap: 6px;
}

.codex-input-actions .btn {
  align-items: center;
  display: inline-flex;
  gap: 6px;
  justify-content: center;
  white-space: nowrap;
}
</style>
