<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import type { CodexEvent, CodexPrompt } from "../types/codex";
import { renderMarkdown, renderMermaidIn } from "../utils/markdownRender";

type AgentSession = {
  cwd: string;
  cwd_relative?: string | null;
  status: "idle" | "running" | "exited" | "failed";
  prompts: CodexPrompt[];
  events: CodexEvent[];
};
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

const emit = defineEmits<{
  "open-link": [target: string];
}>();

const props = defineProps<{
  session: AgentSession | null;
  providerName: string;
  providerSessionId?: string | null;
  idleIcon: string;
  idleText: string;
  runningText?: string;
  error?: string;
  focusMode?: boolean;
  showRawJson?: boolean;
  mutedAlpha?: number;
}>();

const scroller = ref<HTMLElement | null>(null);
const transcriptStyle = computed(() => {
  const alpha = clampAlpha(props.mutedAlpha);
  const scaledAlpha = (scale: number) => Math.min(1, Math.max(0, alpha * scale)).toFixed(3);
  return {
    "--agent-muted-alpha": alpha.toFixed(3),
    "--agent-muted-text": `rgb(37 50 74 / ${scaledAlpha(0.72)})`,
    "--agent-muted-strong": `rgb(37 50 74 / ${scaledAlpha(0.86)})`,
    "--agent-muted-faint": `rgb(37 50 74 / ${scaledAlpha(0.5)})`,
    "--agent-muted-border": `rgb(118 134 160 / ${scaledAlpha(0.32)})`,
    "--agent-muted-surface": `rgb(255 255 255 / ${Math.min(0.82, 0.2 + alpha * 0.45).toFixed(3)})`,
  };
});
const sortedEvents = computed(() => [...(props.session?.events ?? [])].sort((a, b) => a.index - b.index));
const transcriptEntries = computed<TranscriptEntry[]>(() => {
  const prompts = props.session?.prompts ?? [];
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
    if (props.focusMode && muted) continue;
    const fileChanges = event.file_changes.map((change) => ({
      path: change.path,
      changeType: change.change_type,
      diff: change.diff ?? null,
    }));
    const patchText = event.patch_text ?? null;
    const visibleFileChanges = props.focusMode ? [] : fileChanges;
    const visiblePatchText = props.focusMode ? null : patchText;
    if (text || visibleFileChanges.length || visiblePatchText || (props.showRawJson && event.raw_preview)) {
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

function messageHtml(text: string): string {
  return renderMarkdown(text, { baseDirectory: props.session?.cwd_relative ?? "" });
}

function openAgentLink(event: MouseEvent) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  const element = event.target instanceof Element ? event.target : null;
  const link = element?.closest<HTMLAnchorElement>("a[data-viewer-link]");
  const target = link?.dataset.viewerTarget;
  if (!target) return;
  event.preventDefault();
  emit("open-link", target);
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
  await renderMermaidIn(scroller.value, "agent-mermaid");
}

watch(transcriptEntries, () => {
  void renderRichContent();
});

defineExpose({ scrollToBottom, renderRichContent });
</script>

<template>
  <div ref="scroller" class="agent-transcript-scroll" :style="transcriptStyle" @click="openAgentLink">
    <div v-if="error" class="agent-error">{{ error }}</div>

    <div v-if="session" class="session-meta">
      <span>{{ providerSessionId || "not started" }}</span>
      <span>{{ session.cwd }}</span>
    </div>

    <div v-if="session?.status === 'idle'" class="empty-agent">
      <i class="bi" :class="idleIcon"></i>
      <span>{{ idleText }}</span>
    </div>

    <template v-for="entry in transcriptEntries" :key="entry.id">
      <div v-if="entry.kind === 'prompt'" class="message user-message">
        <div class="message-text markdown-content" v-html="messageHtml(entry.text)"></div>
      </div>

      <div v-else class="message event-message" :class="{ 'event-message-muted': isMutedEvent(entry) }">
        <div v-if="entry.text" class="message-text markdown-content" v-html="messageHtml(entry.text)"></div>
        <pre v-if="showRawJson && entry.event.raw_preview" class="raw-json">{{ JSON.stringify(entry.event.raw_preview, null, 2) }}</pre>
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
      <span>{{ runningText ?? `${providerName} is running` }}</span>
    </div>
  </div>
</template>

<style scoped>
.agent-transcript-scroll {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
  padding: 12px;
  user-select: text;
}

.session-meta {
  color: var(--agent-muted-faint);
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
  color: var(--agent-muted-text);
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
  --markdown-body-color: var(--agent-muted-text);
  --markdown-border-color: var(--agent-muted-border);
  --markdown-code-background: transparent;
  --markdown-code-color: var(--agent-muted-strong);
  --markdown-h1-color: var(--agent-muted-strong);
  --markdown-h2-color: var(--agent-muted-strong);
  --markdown-h3-color: var(--agent-muted-strong);
  --markdown-h4-color: var(--agent-muted-strong);
  --markdown-link-color: var(--agent-muted-strong);
  --markdown-paragraph-color: var(--agent-muted-text);
  --syntax-background: var(--agent-muted-surface);
  --syntax-text: var(--agent-muted-text);
  color: var(--agent-muted-text);
  font-size: 11px;
  line-height: 1.28;
}

.event-message-muted .message-text :deep(p),
.event-message-muted .message-text :deep(li),
.event-message-muted .message-text :deep(pre),
.event-message-muted .message-text :deep(code) {
  color: var(--agent-muted-text);
}

.file-changes {
  color: var(--agent-muted-text);
  font-size: 10px;
  margin-top: 2px;
}

.file-change {
  border-left: 2px solid var(--agent-muted-border);
  margin-top: 2px;
  padding-left: 5px;
}

.file-change-path {
  color: var(--agent-muted-text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10px;
}

.file-change-diff {
  background: var(--agent-muted-surface);
  border: 1px solid var(--agent-muted-border);
  border-radius: 6px;
  color: var(--agent-muted-text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10px;
  line-height: 1.24;
  margin: 2px 0 0;
  overflow-wrap: anywhere;
  padding: 3px 0;
  white-space: pre-wrap;
}

.patch-input {
  color: var(--agent-muted-text);
  font-size: 10px;
  margin-top: 3px;
}

.patch-input-title,
.file-change-result-title {
  color: var(--agent-muted-text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10px;
  margin-bottom: 2px;
}

.file-change-result {
  margin-top: 2px;
}

.file-change-result pre,
.raw-json {
  background: var(--agent-muted-surface);
  border: 1px solid var(--agent-muted-border);
  border-radius: 6px;
  color: var(--agent-muted-text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10px;
  line-height: 1.24;
  margin: 0;
  overflow-wrap: anywhere;
  padding: 3px 5px;
  white-space: pre-wrap;
}

.raw-json {
  margin-top: 4px;
  max-height: 260px;
  overflow: auto;
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
  color: var(--agent-muted-text);
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

.agent-error {
  background: #fff1f1;
  border: 1px solid #efc7c7;
  border-radius: 8px;
  color: #a33;
  margin-bottom: 10px;
  padding: 9px;
}

.running-row {
  align-items: center;
  color: var(--agent-muted-faint);
  display: flex;
  font-size: 11px;
  gap: 6px;
  padding: 3px 2px;
}

.empty-agent {
  align-items: center;
  color: var(--text-muted);
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 150px;
  justify-content: center;
  text-align: center;
}

.empty-agent .bi {
  font-size: 28px;
}
</style>
