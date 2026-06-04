<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { getTree } from "../api/client";
import type { FileEntry } from "../types/files";

const model = defineModel<string>({ required: true });
const error = ref("");
const loading = ref(false);
const directoryOptions = ref<Record<string, FileEntry[]>>({});
const parts = computed(() => model.value.split("/").map((part) => part.trim()).filter(Boolean));
const levelPaths = computed(() => {
  const paths = [""];
  for (let index = 1; index <= parts.value.length; index += 1) {
    paths.push(parts.value.slice(0, index).join("/"));
  }
  return paths;
});

onMounted(() => {
  void ensureLoadedPathChain();
});

watch(
  () => model.value,
  () => {
    void ensureLoadedPathChain();
  },
);

async function ensureLoadedPathChain() {
  await Promise.all(levelPaths.value.map((path) => loadOptions(path)));
}

async function loadOptions(path: string) {
  if (directoryOptions.value[path]) return;
  loading.value = true;
  error.value = "";
  try {
    const listing = await getTree(path);
    directoryOptions.value = {
      ...directoryOptions.value,
      [path]: listing.entries.filter((entry) => entry.is_dir).sort((left, right) => left.name.localeCompare(right.name, undefined, { sensitivity: "base" })),
    };
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    directoryOptions.value = { ...directoryOptions.value, [path]: [] };
  } finally {
    loading.value = false;
  }
}

function valueForLevel(index: number) {
  return parts.value[index] ?? "";
}

function optionsForParent(parentPath: string) {
  return directoryOptions.value[parentPath] ?? [];
}

function updateLevel(index: number, value: string) {
  const nextParts = parts.value.slice(0, index);
  if (value) nextParts.push(value);
  model.value = nextParts.join("/");
}

function updateLevelFromEvent(index: number, event: Event) {
  updateLevel(index, (event.target as HTMLSelectElement | null)?.value ?? "");
}

function clearPath() {
  model.value = "";
}
</script>

<template>
  <div class="directory-picker">
    <div class="directory-picker-row">
      <template v-for="(parentPath, index) in levelPaths" :key="`${index}:${parentPath}`">
        <select class="form-select form-select-sm directory-select" :value="valueForLevel(index)" @change="updateLevelFromEvent(index, $event)">
          <option value="">{{ index === 0 ? "Profile root" : "Use this directory" }}</option>
          <option v-for="entry in optionsForParent(parentPath)" :key="entry.path" :value="entry.name">{{ entry.name }}</option>
        </select>
      </template>
      <button class="btn btn-sm btn-outline-secondary icon-button" type="button" title="Use profile root" :disabled="!model" @click="clearPath">
        <i class="bi bi-house"></i>
      </button>
    </div>
    <div class="directory-picker-meta">
      <span>{{ model || "Profile root" }}</span>
      <span v-if="loading">Loading</span>
      <span v-if="error" class="directory-error">{{ error }}</span>
    </div>
  </div>
</template>

<style scoped>
.directory-picker {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.directory-picker-row {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.directory-select {
  flex: 1 1 150px;
  max-width: 240px;
  min-width: 130px;
}

.directory-picker-meta {
  color: var(--text-muted);
  display: flex;
  flex-wrap: wrap;
  font-size: 12px;
  gap: 8px;
  min-height: 16px;
  overflow-wrap: anywhere;
}

.directory-error {
  color: #a33;
}
</style>
