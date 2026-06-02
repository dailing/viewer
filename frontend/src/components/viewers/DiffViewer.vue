<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { commitGit, getGitDiff, pushGit, revertGitPath, stageGitPath } from "../../api/client";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { fileChangeAffectsPath } from "../../utils/paths";
import type { WatchEvent } from "../../types/files";

const props = defineProps<{ path: string; cwd: string; paneId: string }>();
const toolbar = usePaneToolbarStore();
type DiffMode = "normal" | "word" | "split";
type DiffLineKind = "add" | "delete" | "hunk" | "file" | "context";
type DiffSegment = { text: string; changed: boolean };
type DiffLine = { text: string; kind: DiffLineKind; segments: DiffSegment[] };
type SplitRow = { kind: DiffLineKind | "empty"; left: DiffLine | null; right: DiffLine | null; full?: DiffLine };

const diff = ref("");
const isBinary = ref(false);
const loading = ref(false);
const error = ref("");
const message = ref("");
const mode = ref<DiffMode>("normal");
const container = ref<HTMLElement | null>(null);

const wordDiffLines = computed(() => diffLines(diff.value));
const splitRows = computed(() => toSplitRows(wordDiffLines.value));

function setMessage(value: string) {
  message.value = value;
  window.setTimeout(() => {
    if (message.value === value) message.value = "";
  }, 2600);
}

async function load() {
  loading.value = true;
  error.value = "";
  diff.value = "";
  isBinary.value = false;
  try {
    const result = await getGitDiff(props.path);
    diff.value = result.diff;
    isBinary.value = result.is_binary;
    await nextTick();
    if (container.value) container.value.scrollTop = 0;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    diff.value = "";
  } finally {
    loading.value = false;
    registerToolbar();
  }
}

async function runAction(action: () => Promise<unknown>, success: string) {
  error.value = "";
  try {
    await action();
    setMessage(success);
    await load();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function stageFile() {
  await runAction(() => stageGitPath(props.path), "Staged file");
}

async function stageAll() {
  await runAction(() => stageGitPath(undefined, props.path), "Staged all changes");
}

async function revertFile() {
  if (!window.confirm(`Revert changes in ${props.path}?`)) return;
  await runAction(() => revertGitPath(props.path), "Reverted file");
}

async function commitChanges() {
  const commitMessage = window.prompt("Commit message");
  if (!commitMessage) return;
  await runAction(() => commitGit(commitMessage, props.path), "Committed changes");
}

async function pushChanges() {
  await runAction(() => pushGit(props.path), "Pushed branch");
}

function setMode(value: DiffMode) {
  mode.value = value;
  registerToolbar();
}

function registerToolbar() {
  toolbar.setPaneToolbar(props.paneId, {
    title: `Diff: ${props.path}`,
    status: isBinary.value ? "binary" : message.value || undefined,
    statusClass: isBinary.value ? "pane-status-warning" : undefined,
    actions: [
      { id: "mode-normal", title: "Normal diff", label: "Diff", active: mode.value === "normal", run: () => setMode("normal") },
      { id: "mode-word", title: "Word diff", label: "Word", active: mode.value === "word", run: () => setMode("word") },
      { id: "mode-split", title: "Side-by-side diff", label: "Split", active: mode.value === "split", run: () => setMode("split") },
      { id: "stage-file", title: "Stage file", icon: "bi-plus-square", run: stageFile },
      { id: "stage-all", title: "Stage all changes", label: "All", run: stageAll },
      { id: "revert-file", title: "Revert file", icon: "bi-arrow-counterclockwise", variant: "danger", run: revertFile },
      { id: "commit", title: "Commit staged changes", icon: "bi-check2-square", run: commitChanges },
      { id: "push", title: "Push", icon: "bi-cloud-arrow-up", run: pushChanges },
    ],
  });
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

function withWordDiffs(lines: DiffLine[]): DiffLine[] {
  const next = lines.map((line) => ({ ...line, segments: [...line.segments] }));
  for (let index = 0; index < next.length; index += 1) {
    if (next[index].kind !== "delete") continue;
    const deleteStart = index;
    while (index < next.length && next[index].kind === "delete") index += 1;
    const deleteLines = next.slice(deleteStart, index);
    if (next[index]?.kind !== "add") {
      for (const line of deleteLines) line.segments = [{ text: line.text || " ", changed: true }];
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
    if (previous && previous.changed === segment.changed) previous.text += segment.text;
    else merged.push({ ...segment });
  }
  return merged.length ? merged : [{ text: " ", changed: false }];
}

function toSplitRows(lines: DiffLine[]): SplitRow[] {
  const rows: SplitRow[] = [];
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    if (line.kind === "file" || line.kind === "hunk") {
      rows.push({ kind: line.kind, left: null, right: null, full: line });
      continue;
    }
    if (line.kind === "context") {
      rows.push({ kind: "context", left: line, right: line });
      continue;
    }
    if (line.kind === "delete") {
      const deletes: DiffLine[] = [];
      const adds: DiffLine[] = [];
      while (lines[index]?.kind === "delete") {
        deletes.push(lines[index]);
        index += 1;
      }
      while (lines[index]?.kind === "add") {
        adds.push(lines[index]);
        index += 1;
      }
      index -= 1;
      const count = Math.max(deletes.length, adds.length);
      for (let rowIndex = 0; rowIndex < count; rowIndex += 1) {
        rows.push({ kind: deletes[rowIndex] && adds[rowIndex] ? "context" : deletes[rowIndex] ? "delete" : "add", left: deletes[rowIndex] ?? null, right: adds[rowIndex] ?? null });
      }
      continue;
    }
    rows.push({ kind: "add", left: null, right: line });
  }
  return rows;
}

function sideSegments(line: DiffLine | null): DiffSegment[] {
  if (!line) return [{ text: " ", changed: false }];
  if (line.kind !== "add" && line.kind !== "delete" && line.kind !== "context") return line.segments;
  const markerLength = line.text[0] === " " || line.text[0] === "+" || line.text[0] === "-" ? 1 : 0;
  if (!markerLength) return line.segments;
  return stripLeadingCharacters(line.segments, markerLength);
}

function stripLeadingCharacters(segments: DiffSegment[], count: number): DiffSegment[] {
  const stripped: DiffSegment[] = [];
  let remaining = count;
  for (const segment of segments) {
    if (remaining >= segment.text.length) {
      remaining -= segment.text.length;
      continue;
    }
    const text = remaining > 0 ? segment.text.slice(remaining) : segment.text;
    remaining = 0;
    if (text) stripped.push({ text, changed: segment.changed });
  }
  return stripped.length ? stripped : [{ text: " ", changed: false }];
}

function handleChange(event: Event) {
  const detail = (event as CustomEvent<WatchEvent>).detail;
  if (fileChangeAffectsPath(detail.path, props.path)) void load();
}

function handleRefresh(event: Event) {
  const paneId = (event as CustomEvent<{ paneId?: string }>).detail?.paneId;
  if (paneId === props.paneId) void load();
}

watch(() => props.path, () => void load(), { immediate: true });
watch(message, registerToolbar);
watch(mode, registerToolbar);

onMounted(() => {
  registerToolbar();
  window.addEventListener("viewer:file-changed", handleChange);
  window.addEventListener("viewer:pane-refresh", handleRefresh);
});
onUnmounted(() => {
  window.removeEventListener("viewer:file-changed", handleChange);
  window.removeEventListener("viewer:pane-refresh", handleRefresh);
  toolbar.clearPaneToolbar(props.paneId);
});
</script>

<template>
  <div class="diff-viewer">
    <div v-if="loading" class="diff-state">
      <div class="spinner-border spinner-border-sm" role="status" aria-label="Loading diff"></div>
    </div>
    <div v-else-if="error" class="diff-state error-state">
      <i class="bi bi-exclamation-triangle"></i>
      <span>{{ error }}</span>
    </div>
    <div v-else-if="isBinary" class="diff-state">
      <i class="bi bi-file-earmark-binary"></i>
      <span>Binary file diff cannot be viewed.</span>
    </div>
    <pre v-else-if="mode === 'normal'" ref="container" class="diff-content diff-word-content markdown-syntax"><span v-for="(line, index) in wordDiffLines" :key="index" class="diff-line" :class="`diff-${line.kind}`"><span v-for="(segment, segmentIndex) in line.segments" :key="segmentIndex" :class="{ 'diff-word-change': segment.changed }">{{ segment.text }}</span></span></pre>
    <pre v-else-if="mode === 'word'" ref="container" class="diff-content diff-word-content markdown-syntax"><span v-for="(line, index) in wordDiffLines" :key="index" class="diff-line" :class="`diff-${line.kind}`"><span v-for="(segment, segmentIndex) in line.segments" :key="segmentIndex" :class="{ 'diff-word-change': segment.changed }">{{ segment.text }}</span></span></pre>
    <div v-else ref="container" class="diff-content diff-split-content markdown-syntax">
      <div v-for="(row, index) in splitRows" :key="index" class="split-row" :class="`split-${row.full?.kind ?? row.kind}`">
        <div v-if="row.full" class="split-full">{{ row.full.text || " " }}</div>
        <template v-else>
          <pre class="split-cell split-left" :class="row.left ? `diff-${row.left.kind}` : 'split-empty'"><span v-for="(segment, segmentIndex) in sideSegments(row.left)" :key="segmentIndex" :class="{ 'diff-word-change': segment.changed }">{{ segment.text }}</span></pre>
          <pre class="split-cell split-right" :class="row.right ? `diff-${row.right.kind}` : 'split-empty'"><span v-for="(segment, segmentIndex) in sideSegments(row.right)" :key="segmentIndex" :class="{ 'diff-word-change': segment.changed }">{{ segment.text }}</span></pre>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.diff-viewer {
  background: var(--syntax-background);
  color: var(--syntax-text);
  height: 100%;
  min-height: 0;
}

.diff-content {
  background: var(--syntax-background);
  color: var(--syntax-text);
  font-size: 12px;
  height: 100%;
  line-height: 1.45;
  margin: 0;
  overflow: auto;
  padding: 12px 14px;
  user-select: text;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
  word-break: break-word;
}

.diff-line {
  display: block;
  min-height: 1.45em;
}

.diff-add {
  background: rgb(34 134 58 / 0.14);
  color: #116329;
}

.diff-delete {
  background: rgb(207 34 46 / 0.13);
  color: #a40e26;
}

.diff-hunk {
  background: rgb(9 105 218 / 0.1);
  color: #0969da;
}

.diff-file {
  color: #8250df;
  font-weight: 700;
}

.diff-word-change {
  background: rgb(255 212 0 / 0.45);
  border-radius: 2px;
}

.diff-add .diff-word-change {
  background: rgb(34 134 58 / 0.26);
}

.diff-delete .diff-word-change {
  background: rgb(207 34 46 / 0.22);
}

.diff-split-content {
  display: grid;
  gap: 0;
  padding: 12px 0;
  white-space: normal;
}

.split-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  min-width: 0;
}

.split-full {
  background: rgb(9 105 218 / 0.1);
  color: #0969da;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  grid-column: 1 / -1;
  line-height: 1.45;
  min-height: 1.45em;
  padding: 0 14px;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
  word-break: break-word;
}

.split-file .split-full {
  background: transparent;
  color: #8250df;
  font-weight: 700;
}

.split-cell {
  border: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.45;
  margin: 0;
  min-height: 1.45em;
  min-width: 0;
  overflow: visible;
  padding: 0 14px;
  white-space: pre-wrap;
  word-break: break-word;
}

.split-left {
  border-right: 1px solid var(--border);
}

.split-empty {
  background: rgb(148 163 184 / 0.08);
}

.diff-state {
  align-items: center;
  color: var(--text-muted);
  display: flex;
  flex-direction: column;
  gap: 8px;
  height: 100%;
  justify-content: center;
  padding: 18px;
  text-align: center;
}

.diff-state .bi {
  font-size: 28px;
}

.error-state {
  color: #a33;
}
</style>
