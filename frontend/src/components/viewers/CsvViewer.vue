<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref } from "vue";
import { getText } from "../../api/client";
import { useReloadingScrollMemory } from "../../composables/useScrollMemory";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { restoreScrollPosition } from "../../utils/scrollMemory";

type CsvMode = "table" | "raw";

const props = defineProps<{ path: string; version: number; paneId: string }>();
const toolbar = usePaneToolbarStore();
const mode = ref<CsvMode>("table");
const text = ref("");
const rows = ref<string[][]>([]);
const error = ref("");
const container = ref<HTMLElement | null>(null);

const columnCount = computed(() => rows.value.reduce((max, row) => Math.max(max, row.length), 0));
const headerRow = computed(() => rows.value[0] ?? []);
const bodyRows = computed(() => rows.value.slice(1));

function setMode(value: CsvMode) {
  mode.value = value;
  registerToolbar();
  void nextTick(async () => {
    await restoreScrollPosition(props.path, container.value);
  });
}

function registerToolbar() {
  toolbar.setPaneToolbar(props.paneId, {
    title: props.path,
    actions: [
      { id: "csv-table", title: "Table view", label: "Table", active: mode.value === "table", run: () => setMode("table") },
      { id: "csv-raw", title: "Raw CSV", label: "Raw", active: mode.value === "raw", run: () => setMode("raw") },
    ],
  });
}

function parseCsv(value: string): string[][] {
  const parsed: string[][] = [];
  let row: string[] = [];
  let cell = "";
  let inQuotes = false;

  for (let index = 0; index < value.length; index += 1) {
    const char = value[index];
    const next = value[index + 1];

    if (char === "\"") {
      if (inQuotes && next === "\"") {
        cell += "\"";
        index += 1;
      } else {
        inQuotes = !inQuotes;
      }
      continue;
    }

    if (char === "," && !inQuotes) {
      row.push(cell);
      cell = "";
      continue;
    }

    if ((char === "\n" || char === "\r") && !inQuotes) {
      row.push(cell);
      parsed.push(row);
      row = [];
      cell = "";
      if (char === "\r" && next === "\n") index += 1;
      continue;
    }

    cell += char;
  }

  row.push(cell);
  parsed.push(row);

  while (parsed.length && parsed[parsed.length - 1].every((value) => value === "")) {
    parsed.pop();
  }
  return parsed;
}

async function load() {
  error.value = "";
  try {
    text.value = await getText(props.path);
    rows.value = parseCsv(text.value);
    registerToolbar();
    await nextTick();
    await restoreScrollPosition(props.path, container.value);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

const { saveCurrentScroll } = useReloadingScrollMemory(
  () => props.path,
  () => props.version,
  container,
  load,
);

onUnmounted(() => {
  toolbar.clearPaneToolbar(props.paneId);
});
</script>

<template>
  <div v-if="!error && mode === 'table'" ref="container" class="csv-table-shell" @scroll.passive="saveCurrentScroll">
    <table v-if="rows.length" class="csv-table">
      <thead>
        <tr>
          <th v-for="columnIndex in columnCount" :key="columnIndex">{{ headerRow[columnIndex - 1] }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(row, rowIndex) in bodyRows" :key="rowIndex">
          <td v-for="columnIndex in columnCount" :key="columnIndex">{{ row[columnIndex - 1] }}</td>
        </tr>
      </tbody>
    </table>
    <div v-else class="csv-empty">Empty CSV</div>
  </div>
  <pre v-else-if="!error" ref="container" class="csv-raw" @scroll.passive="saveCurrentScroll">{{ text }}</pre>
  <div v-else class="csv-error">{{ error }}</div>
</template>

<style scoped>
.csv-table-shell {
  height: 100%;
  overflow: auto;
}

.csv-table {
  border-collapse: separate;
  border-spacing: 0;
  color: var(--text);
  font-size: 12px;
  min-width: 100%;
}

.csv-table th,
.csv-table td {
  border-bottom: 1px solid var(--border);
  border-right: 1px solid var(--border);
  max-width: 420px;
  min-width: 90px;
  padding: 6px 8px;
  text-align: left;
  vertical-align: top;
  white-space: pre-wrap;
  word-break: break-word;
}

.csv-table th {
  background: #f5f7fa;
  font-weight: 700;
  position: sticky;
  top: 0;
  z-index: 1;
}

.csv-table th:first-child,
.csv-table td:first-child {
  border-left: 1px solid var(--border);
}

.csv-table tbody tr:hover td {
  background: #f8fbff;
}

.csv-raw {
  background: var(--syntax-background);
  color: var(--syntax-text);
  height: 100%;
  margin: 0;
  overflow: auto;
  padding: 14px;
  user-select: text;
  white-space: pre;
}

.csv-empty,
.csv-error {
  color: var(--text-muted);
  padding: 14px;
}

.csv-error {
  color: #a33;
}
</style>
