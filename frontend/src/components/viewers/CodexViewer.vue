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
const stopping = ref(false);

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
const codexStatusItems = computed(() => {
  const status = codex.status;
  if (!status.available) return [];
  const items: string[] = [];
  if (status.model) items.push(status.model);
  if (typeof status.context_used_percent === "number") items.push(`ctx ${status.context_used_percent}%`);
  if (typeof status.primary_used_percent === "number" && status.primary_window_minutes) {
    const hours = Math.round(status.primary_window_minutes / 60);
    items.push(`${status.primary_used_percent}%/${hours}h`);
  }
  if (typeof status.secondary_used_percent === "number" && status.secondary_window_minutes) {
    const days = Math.round(status.secondary_window_minutes / (60 * 24));
    items.push(`${status.secondary_used_percent}%/${days}d`);
  }
  if (status.cwd) items.push(status.cwd);
  return items;
});
type TranscriptEntry =
  | { kind: "prompt"; id: string; text: string }
  | { kind: "event"; id: string; event: CodexEvent; text: string; eventType: string; fileChanges: FileChange[]; patchText: string | null };
type FileChange = { path: string; changeType: string; diff: string | null };
type DiffLineKind = "add" | "delete" | "hunk" | "file" | "context";
type DiffSegment = { text: string; changed: boolean };
type DiffLine = { text: string; kind: DiffLineKind; segments: DiffSegment[] };
const transcriptEntries = computed<TranscriptEntry[]>(() => {
  const prompts = session.value?.prompts ?? [];
  const groups: CodexEvent[][] = [];
  let current: CodexEvent[] | null = null;

  for (const event of sortedEvents.value) {
    const eventType = rawType(event.raw);
    if (eventType === "task_started" || eventType === "turn.started") {
      current = [];
      groups.push(current);
      continue;
    }
    if (!current) {
      current = [];
      groups.push(current);
    }
    current.push(event);
    if (eventType === "task_complete" || eventType === "turn_aborted" || eventType === "turn.completed") current = null;
  }

  const entries: TranscriptEntry[] = [];
  const count = Math.max(prompts.length, groups.length);
  for (let index = 0; index < count; index += 1) {
    const prompt = prompts[index];
    if (prompt) entries.push({ kind: "prompt", id: `prompt-${index}`, text: prompt.text });
    const group = groups[index] ?? [];
    const assistantResponseTexts = new Set(
      group
        .filter((event) => isAssistantResponseItem(event.raw))
        .map((event) => normalizeMessageText(eventText(event)))
        .filter(Boolean),
    );
    for (const event of group) {
      const eventType = rawType(event.raw);
      if (isHiddenEventType(eventType)) continue;
      const text = eventText(event);
      if (isDuplicateAgentMessage(event.raw, text, assistantResponseTexts)) continue;
      const fileChanges = extractFileChanges(event.raw);
      const patchText = extractPatchInput(event.raw);
      if (text || showRaw.value || fileChanges.length || patchText) {
        entries.push({ kind: "event", id: `event-${event.index}`, event, text, eventType, fileChanges, patchText });
      }
    }
  }
  return entries;
});

function isHiddenEventType(type: string): boolean {
  return [
    "session_meta",
    "turn_context",
    "task_started",
    "task_complete",
    "turn_aborted",
    "context_compacted",
    "token_count",
    "user_message",
    "turn.started",
    "turn.completed",
    "thread.started",
    "message:developer",
    "message:system",
    "message:user",
    "function_call_output",
    "custom_tool_call_output",
    "web_search_call",
    "web_search_end",
  ].includes(type);
}

function rawType(raw: Record<string, unknown>): string {
  const payload = raw.payload;
  if ((raw.type === "event_msg" || raw.type === "response_item") && payload && typeof payload === "object") {
    const record = payload as Record<string, unknown>;
    if (record.type === "message" && typeof record.role === "string") return `message:${record.role}`;
    if (typeof record.type === "string") return record.type;
  }
  if (raw.type === "custom_tool_call" && typeof raw.name === "string") return `tool:${raw.name}`;
  const item = raw.item;
  if (item && typeof item === "object" && "type" in item && typeof item.type === "string") return item.type;
  const direct = raw.type;
  if (typeof direct === "string") return direct;
  const nested = raw.msg;
  if (nested && typeof nested === "object" && "type" in nested && typeof nested.type === "string") return nested.type;
  return "event";
}

function normalizeMessageText(text: string): string {
  return text.replace(/\r\n/g, "\n").trim();
}

function isAssistantResponseItem(raw: Record<string, unknown>): boolean {
  const payload = payloadOf(raw);
  return raw.type === "response_item" && payload?.type === "message" && payload.role === "assistant";
}

function isDuplicateAgentMessage(raw: Record<string, unknown>, text: string, assistantResponseTexts: Set<string>): boolean {
  const payload = payloadOf(raw);
  return raw.type === "event_msg" && payload?.type === "agent_message" && assistantResponseTexts.has(normalizeMessageText(text));
}

function extractFileChanges(raw: Record<string, unknown>): FileChange[] {
  const direct = raw.type === "patch_apply_end" ? raw : null;
  const wrapped =
    (raw.type === "event_msg" || raw.type === "response_item") &&
    raw.payload &&
    typeof raw.payload === "object" &&
    (raw.payload as Record<string, unknown>).type === "patch_apply_end"
      ? (raw.payload as Record<string, unknown>)
      : null;
  const source = direct ?? wrapped;
  if (!source) return [];
  const changes = source.changes;
  if (!changes || typeof changes !== "object") return [];
  const rows: FileChange[] = [];
  for (const [path, value] of Object.entries(changes as Record<string, unknown>)) {
    const record = value && typeof value === "object" ? (value as Record<string, unknown>) : {};
    rows.push({
      path,
      changeType: typeof record.type === "string" ? record.type : "update",
      diff: typeof record.unified_diff === "string" ? record.unified_diff : null,
    });
  }
  return rows;
}

function extractPatchInput(raw: Record<string, unknown>): string | null {
  const isDirect = raw.type === "custom_tool_call" && raw.name === "apply_patch" && typeof raw.input === "string";
  const wrapped =
    raw.type === "response_item" &&
    raw.payload &&
    typeof raw.payload === "object" &&
    (raw.payload as Record<string, unknown>).type === "custom_tool_call" &&
    (raw.payload as Record<string, unknown>).name === "apply_patch" &&
    typeof (raw.payload as Record<string, unknown>).input === "string";
  if (isDirect) return raw.input as string;
  if (wrapped) return (raw.payload as Record<string, unknown>).input as string;
  return null;
}

function payloadOf(value: Record<string, unknown>): Record<string, unknown> | null {
  return value.payload && typeof value.payload === "object" ? (value.payload as Record<string, unknown>) : null;
}

function contentText(content: unknown): string {
  if (!Array.isArray(content)) return "";
  return content
    .map((item) => {
      if (!item || typeof item !== "object") return "";
      const record = item as Record<string, unknown>;
      if (typeof record.text === "string") return record.text;
      if (typeof record.output_text === "string") return record.output_text;
      return "";
    })
    .filter(Boolean)
    .join("\n");
}

function commandText(command: unknown): string {
  if (Array.isArray(command)) return command.map(String).join(" ");
  if (typeof command === "string") return command;
  return "";
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

function textFrom(value: unknown, depth = 0): string {
  if (depth > 6 || value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (Array.isArray(value)) return value.map((item) => textFrom(item, depth + 1)).filter(Boolean).join("\n");
  if (typeof value !== "object") return "";

  const record = value as Record<string, unknown>;
  if (record.type === "event_msg") {
    const payload = payloadOf(record);
    if (!payload) return "";
    const payloadType = typeof payload.type === "string" ? payload.type : "";
    if (payloadType === "exec_command_begin") {
      const command = commandText(payload.command);
      return command ? `$ ${command}` : "";
    }
    if (payloadType === "exec_command_end") {
      const command = commandText(payload.command);
      const output = typeof payload.aggregated_output === "string" ? payload.aggregated_output.trim() : "";
      const exitCode = typeof payload.exit_code === "number" ? `exit ${payload.exit_code}` : "";
      return [`$ ${command}`.trim(), output, exitCode].filter(Boolean).join("\n");
    }
    if (payloadType === "agent_message") {
      const message = payload.message;
      if (typeof message === "string") return message;
    }
    if (payloadType === "patch_apply_end") {
      const success = payload.success === true ? "Applied patch" : "Patch failed";
      const stdout = typeof payload.stdout === "string" ? payload.stdout.trim() : "";
      const stderr = typeof payload.stderr === "string" ? payload.stderr.trim() : "";
      return [success, stdout, stderr].filter(Boolean).join("\n");
    }
    if (payloadType === "view_image_tool_call" && typeof payload.path === "string") return `Viewed image: ${payload.path}`;
  }
  if (record.type === "response_item") {
    const payload = payloadOf(record);
    if (!payload) return "";
    if (payload.type === "message") {
      const role = typeof payload.role === "string" ? payload.role : "";
      if (role !== "assistant") return "";
      return contentText(payload.content);
    }
    if (payload.type === "function_call" && typeof payload.name === "string") {
      if (payload.name === "exec_command" && typeof payload.arguments === "string") {
        try {
          const args = JSON.parse(payload.arguments) as Record<string, unknown>;
          if (typeof args.cmd === "string") return `$ ${args.cmd}`;
        } catch {
          return `Tool call: ${payload.name}`;
        }
      }
      return `Tool call: ${payload.name}`;
    }
    if (payload.type === "custom_tool_call" && typeof payload.name === "string") {
      if (payload.name === "apply_patch") return "Applied patch";
      return `Tool call: ${payload.name}`;
    }
    return "";
  }
  if (record.type === "custom_tool_call" && typeof record.name === "string") {
    if (record.name === "apply_patch") return "Applied patch";
    return `Tool call: ${record.name}`;
  }
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
  if (!prompt || !session.value) return;
  error.value = "";
  promptText.value = "";
  session.value.prompts.push({ text: prompt, created_at: Date.now() / 1000 });
  try {
    applyInfo(await codex.send(props.id, prompt));
    scrollToBottom(true);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
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

function updatePaneToolbar() {
  const status = session.value?.status ?? "connecting";
  const actions: PaneToolbarAction[] = [
    {
      id: "codex-refresh",
      title: "Refresh Codex messages",
      icon: "bi-arrow-clockwise",
      run: () => loadSnapshot(),
    },
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
        onChange: (value) => codex.setSelectedModel(value),
      },
      {
        kind: "chips",
        id: "codex-status",
        title: codex.status.rollout_path ?? undefined,
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
watch(showRaw, updatePaneToolbar);
watch(() => [codex.models.selected_model, codex.models.available_models, codex.status], updatePaneToolbar, { deep: true });
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
          <div class="message-text markdown-content" v-html="messageHtml(entry.text)"></div>
        </div>

        <div v-else class="message event-message">
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
      <div class="codex-input-box">
        <textarea v-model="promptText" rows="3" placeholder="Send a message to this Codex session"></textarea>
        <VoiceInputButton v-model="promptText" />
      </div>
      <div class="codex-input-actions">
        <button class="btn btn-primary" type="submit" :disabled="!canSend">
          <i class="bi bi-send-fill"></i>
          <span>Send</span>
        </button>
        <button class="btn btn-outline-danger" type="button" :disabled="session?.status !== 'running' || stopping" @click="stopRun">
          <i class="bi bi-stop-fill"></i>
          <span>{{ stopping ? "Stopping" : "Stop" }}</span>
        </button>
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
  color: var(--text-muted);
  display: flex;
  flex-wrap: wrap;
  font-size: 11px;
  gap: 8px;
  margin-bottom: 10px;
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

.file-changes {
  margin-top: 6px;
}

.file-change {
  border-left: 2px solid #d8e0ed;
  margin-top: 6px;
  padding-left: 8px;
}

.file-change-path {
  color: #3b4d68;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
}

.file-change-diff {
  background: #f4f7fb;
  border: 1px solid #dde5f1;
  border-radius: 6px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 11px;
  line-height: 1.35;
  margin: 6px 0 0;
  max-height: 260px;
  overflow: auto;
  overflow-wrap: anywhere;
  padding: 6px 0;
  white-space: pre-wrap;
}

.patch-input {
  margin-top: 8px;
}

.patch-input-title {
  color: #3b4d68;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  margin-bottom: 4px;
}

.file-change-result {
  margin-top: 6px;
}

.file-change-result-title {
  color: #3b4d68;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  margin-bottom: 4px;
}

.file-change-result pre {
  background: #ffffff;
  border: 1px solid #dde5f1;
  border-radius: 6px;
  color: #25324a;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 11px;
  line-height: 1.35;
  margin: 0;
  max-height: 260px;
  overflow: auto;
  overflow-wrap: anywhere;
  padding: 6px 8px;
  white-space: pre-wrap;
}

.diff-line {
  display: block;
  min-height: 1.35em;
  padding: 0 8px;
  user-select: text;
}

.diff-add {
  background: #e8f6ec;
  color: #1a6b32;
}

.diff-delete {
  background: #ffebe9;
  color: #b42318;
}

.diff-hunk {
  background: #edf4ff;
  color: #345f9f;
}

.diff-file {
  color: #5d6b82;
  font-weight: 600;
}

.diff-add .diff-word-change {
  background: #b7e7c1;
}

.diff-delete .diff-word-change {
  background: #ffc9c2;
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
  background: #ffffff;
  border-top: 1px solid var(--border);
  display: grid;
  flex: 0 0 auto;
  gap: 7px;
  padding: 8px;
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
