<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { getTree } from "../api/client";
import type { FileEntry } from "../types/files";

const model = defineModel<string>({ required: true });
const props = withDefaults(
  defineProps<{
    emptyLabel?: string;
    clearTitle?: string;
  }>(),
  {
    emptyLabel: "Profile home",
    clearTitle: "Clear working directory",
  },
);
const error = ref("");
const loading = ref(false);
const directoryOptions = ref<Record<string, FileEntry[]>>({});
const open = ref(false);
const highlightedIndex = ref(0);
const inputValue = ref(model.value);
const normalizedValue = computed(() => normalizePathInput(inputValue.value));
const currentParent = computed(() => parentPathFor(normalizedValue.value));
const currentToken = computed(() => tokenFor(normalizedValue.value));
const currentOptions = computed(() => directoryOptions.value[currentParent.value] ?? []);
const filteredOptions = computed(() => {
  const token = currentToken.value.toLowerCase();
  const options = currentOptions.value.filter((entry) => !token || entry.name.toLowerCase().startsWith(token));
  return options.slice(0, 80);
});
const showOptions = computed(() => open.value);

onMounted(() => {
  void loadOptions(currentParent.value);
});

watch(
  () => model.value,
  (value) => {
    if (pathForModel(inputValue.value) !== value) inputValue.value = value;
  },
);

watch(
  () => currentParent.value,
  () => {
    highlightedIndex.value = 0;
    void loadOptions(currentParent.value);
  },
);

watch(filteredOptions, (options) => {
  highlightedIndex.value = Math.min(Math.max(0, highlightedIndex.value), Math.max(0, options.length - 1));
});

async function loadOptions(path: string) {
  const validation = validationMessage(inputValue.value);
  if (validation) {
    error.value = validation;
    return;
  }
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

function normalizePathInput(value: string) {
  return value.replace(/\\/g, "/").replace(/\/{2,}/g, "/");
}

function pathForModel(value: string) {
  return normalizePathInput(value).trim().replace(/\/+$/, "");
}

function validationMessage(value: string) {
  const normalized = normalizePathInput(value).trim();
  if (normalized.startsWith("/")) return "Absolute paths are not allowed";
  if (normalized.split("/").some((part) => part === "..")) return ".. path segments are not allowed";
  return "";
}

function parentPathFor(value: string) {
  const normalized = normalizePathInput(value);
  if (!normalized || normalized.endsWith("/")) return normalized.replace(/\/+$/, "");
  const slash = normalized.lastIndexOf("/");
  return slash === -1 ? "" : normalized.slice(0, slash);
}

function tokenFor(value: string) {
  const normalized = normalizePathInput(value);
  if (!normalized || normalized.endsWith("/")) return "";
  const slash = normalized.lastIndexOf("/");
  return slash === -1 ? normalized : normalized.slice(slash + 1);
}

function joinPath(parent: string, child: string) {
  return parent ? `${parent}/${child}` : child;
}

function updateInput(event: Event) {
  const next = normalizePathInput((event.target as HTMLInputElement | null)?.value ?? "");
  inputValue.value = next;
  const validation = validationMessage(next);
  error.value = validation;
  if (!validation) model.value = pathForModel(next);
  open.value = true;
  highlightedIndex.value = 0;
}

function openOptions() {
  open.value = true;
  const validation = validationMessage(inputValue.value);
  if (validation) {
    error.value = validation;
    return;
  }
  void loadOptions(currentParent.value);
}

function closeOptionsSoon() {
  window.setTimeout(() => {
    open.value = false;
    if (validationMessage(inputValue.value)) {
      inputValue.value = model.value;
      error.value = "";
    }
  }, 120);
}

function selectOption(entry: FileEntry) {
  const next = joinPath(currentParent.value, entry.name);
  inputValue.value = `${next}/`;
  model.value = next;
  error.value = "";
  highlightedIndex.value = 0;
  open.value = true;
  void loadOptions(next);
}

function handleKeydown(event: KeyboardEvent) {
  if (event.key === "ArrowDown") {
    event.preventDefault();
    openOptions();
    highlightedIndex.value = Math.min(Math.max(0, filteredOptions.value.length - 1), highlightedIndex.value + 1);
    return;
  }
  if (event.key === "ArrowUp") {
    event.preventDefault();
    highlightedIndex.value = Math.max(0, highlightedIndex.value - 1);
    return;
  }
  if (event.key === "Enter" && open.value && filteredOptions.value[highlightedIndex.value]) {
    event.preventDefault();
    selectOption(filteredOptions.value[highlightedIndex.value]);
    return;
  }
  if (event.key === "Escape") {
    open.value = false;
  }
}

function clearPath() {
  inputValue.value = "";
  model.value = "";
  error.value = "";
  highlightedIndex.value = 0;
  open.value = true;
  void loadOptions("");
}
</script>

<template>
  <div class="directory-picker">
    <div class="directory-picker-row" @focusout="closeOptionsSoon">
      <div class="path-combobox">
        <input
          class="form-control form-control-sm path-input"
          :placeholder="props.emptyLabel"
          :value="inputValue"
          autocomplete="off"
          spellcheck="false"
          @focus="openOptions"
          @click="openOptions"
          @input="updateInput"
          @keydown="handleKeydown"
        />
        <div v-if="showOptions" class="path-options" role="listbox">
          <div v-if="loading" class="path-option muted">Loading</div>
          <button
            v-for="(entry, index) in filteredOptions"
            :key="entry.path"
            class="path-option"
            :class="{ highlighted: index === highlightedIndex }"
            type="button"
            role="option"
            @mousedown.prevent="selectOption(entry)"
            @mouseenter="highlightedIndex = index"
          >
            <i class="bi bi-folder"></i>
            <span>{{ entry.name }}</span>
          </button>
          <div v-if="!loading && !filteredOptions.length && !error" class="path-option muted">No directories</div>
          <div v-if="error" class="path-option error">{{ error }}</div>
        </div>
      </div>
      <button class="btn btn-sm btn-outline-secondary clear-cwd-button" type="button" :title="props.clearTitle" :disabled="!model" @mousedown.prevent @click="clearPath">
        <i class="bi bi-house"></i>
        <span>Empty</span>
      </button>
    </div>
    <div class="directory-picker-meta">
      <span>{{ model || props.emptyLabel }}</span>
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
  gap: 6px;
  min-width: 0;
}

.path-combobox {
  flex: 1 1 auto;
  min-width: 0;
  position: relative;
}

.path-input {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
}

.path-options {
  background: var(--color-surface-raised);
  border: 1px solid var(--color-border);
  border-radius: 6px;
  box-shadow: var(--shadow-popover);
  left: 0;
  max-height: 220px;
  overflow: auto;
  padding: 4px;
  position: absolute;
  right: 0;
  top: calc(100% + 4px);
  z-index: 30;
}

.path-option {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: 4px;
  color: inherit;
  display: flex;
  font-size: 12px;
  gap: 7px;
  min-height: 28px;
  padding: 4px 7px;
  text-align: left;
  width: 100%;
}

.path-option.highlighted,
.path-option:hover {
  background: var(--color-surface-hover);
}

.path-option.muted {
  color: var(--color-text-muted);
}

.path-option.error {
  color: var(--color-danger);
}

.clear-cwd-button {
  align-items: center;
  display: inline-flex;
  gap: 5px;
}

.directory-picker-meta {
  color: var(--color-text-muted);
  display: flex;
  flex-wrap: wrap;
  font-size: 12px;
  gap: 8px;
  min-height: 16px;
  overflow-wrap: anywhere;
}

.directory-error {
  color: var(--color-danger);
}
</style>
