<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useFilesStore } from "../../stores/files";
import { useLayoutStore } from "../../stores/layout";
import type { FileEntry } from "../../types/files";
import FileTree from "../FileTree.vue";

const emit = defineEmits<{
  "open-file": [path: string];
}>();

const props = defineProps<{
  defaultCwd?: string;
}>();

const files = useFilesStore();
const layout = useLayoutStore();

const pinned = computed(() => files.pinned);
const currentLabel = computed(() => files.currentPath || props.defaultCwd || "/");
const fileInput = ref<HTMLInputElement | null>(null);
const dragDepth = ref(0);
const uploadError = ref("");
const uploading = ref(false);
const isDragging = computed(() => dragDepth.value > 0);
const canEnterParent = computed(() => Boolean(files.currentPath) && files.currentPath !== (props.defaultCwd ?? ""));

watch(
  () => props.defaultCwd ?? "",
  (cwd) => {
    void enterDefaultDirectory(cwd);
  },
  { immediate: true },
);

async function enterDefaultDirectory(cwd: string) {
  if (files.currentPath === cwd) return;
  uploadError.value = "";
  try {
    await files.enterDirectory(cwd);
  } catch (error) {
    uploadError.value = error instanceof Error ? error.message : String(error);
  }
}

async function openPinned(path: string) {
  try {
    await files.enterDirectory(path);
  } catch {
    emit("open-file", path);
  }
}

function chooseFiles() {
  fileInput.value?.click();
}

async function uploadSelection(fileList: FileList | null | undefined) {
  const selected = Array.from(fileList ?? []).filter((file) => file.name);
  if (!selected.length) return;
  uploadError.value = "";
  uploading.value = true;
  try {
    await files.uploadToCurrent(selected);
  } catch (error) {
    uploadError.value = error instanceof Error ? error.message : String(error);
  } finally {
    uploading.value = false;
  }
}

function onFileInput(event: Event) {
  const input = event.target as HTMLInputElement;
  void uploadSelection(input.files);
  input.value = "";
}

function onDragEnter(event: DragEvent) {
  event.preventDefault();
  dragDepth.value += 1;
}

function onDragOver(event: DragEvent) {
  event.preventDefault();
}

function onDragLeave(event: DragEvent) {
  event.preventDefault();
  dragDepth.value = Math.max(0, dragDepth.value - 1);
}

function onDrop(event: DragEvent) {
  event.preventDefault();
  dragDepth.value = 0;
  void uploadSelection(event.dataTransfer?.files);
}

async function deleteEntry(entry: FileEntry) {
  if (entry.is_dir) return;
  if (!window.confirm(`Delete "${entry.path}"? This cannot be undone.`)) return;
  uploadError.value = "";
  try {
    await files.deletePath(entry.path);
  } catch (error) {
    uploadError.value = error instanceof Error ? error.message : String(error);
  }
}
</script>

<template>
  <div
    class="sidebar-panel files-panel"
    :class="{ dragging: isDragging, uploading }"
    @dragenter="onDragEnter"
    @dragover="onDragOver"
    @dragleave="onDragLeave"
    @drop="onDrop"
  >
    <div class="sidebar-section" v-if="pinned.length">
      <div class="section-title">Pinned</div>
      <div
        v-for="path in pinned"
        :key="path"
        class="sidebar-row"
        :class="{ active: layout.openPaths.includes(path) }"
      >
        <button class="sidebar-row-main" type="button" @click="openPinned(path)">
          <i class="bi bi-pin-angle-fill"></i>
          <span class="sidebar-row-name">{{ path || "/" }}</span>
        </button>
        <button class="btn btn-sm icon-button sidebar-row-action" type="button" title="Unpin" @click="files.togglePin(path)">
          <i class="bi bi-x"></i>
        </button>
      </div>
    </div>

    <div class="sidebar-section tree-section">
      <div class="files-header">
        <button class="btn btn-sm btn-outline-secondary icon-button upload-button" type="button" title="Upload files" :disabled="uploading" @click="chooseFiles">
          <i class="bi" :class="uploading ? 'bi-arrow-repeat' : 'bi-upload'"></i>
        </button>
        <input ref="fileInput" class="file-input" type="file" multiple @change="onFileInput" />
      </div>
      <div class="current-path" :title="currentLabel">
        <i class="bi bi-folder2-open"></i>
        <span>{{ currentLabel }}</span>
      </div>
      <div v-if="isDragging" class="drop-target">
        <i class="bi bi-cloud-arrow-up"></i>
        <span>Drop to upload here</span>
      </div>
      <div v-if="uploadError" class="upload-error" role="alert">{{ uploadError }}</div>
      <button v-if="canEnterParent" class="sidebar-row parent-row" type="button" @click="files.enterParentDirectory()">
        <i class="bi bi-arrow-90deg-up"></i>
        <span class="sidebar-row-name">..</span>
      </button>
      <FileTree
        :entries="files.currentEntries"
        :active-paths="layout.openPaths"
        @open-file="emit('open-file', $event)"
        @delete-file="deleteEntry"
      />
    </div>
  </div>
</template>

<style scoped>
.tree-section {
  border-bottom: 0;
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
}

.files-panel.dragging {
  background: var(--color-accent-soft);
}

.files-header {
  align-items: center;
  display: flex;
  gap: 8px;
  justify-content: flex-end;
  margin-bottom: 6px;
}

.upload-button {
  --nav-button-size: 26px;
  --nav-icon-size: 14px;
  flex: 0 0 auto;
}

.file-input {
  display: none;
}

.current-path {
  align-items: center;
  color: var(--color-text-muted);
  display: flex;
  font-size: 12px;
  gap: 7px;
  margin-bottom: 6px;
  min-width: 0;
  padding: 2px 6px;
}

.current-path span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.drop-target {
  align-items: center;
  background: var(--color-accent-soft);
  border: 1px dashed var(--color-accent);
  border-radius: var(--radius-sm);
  color: var(--color-accent-hover);
  display: flex;
  font-size: 12px;
  gap: 7px;
  margin-bottom: 6px;
  min-height: 34px;
  padding: 7px 8px;
}

.upload-error {
  background: color-mix(in srgb, var(--color-danger) 10%, var(--color-surface));
  border: 1px solid color-mix(in srgb, var(--color-danger) 38%, var(--color-border));
  border-radius: var(--radius-sm);
  color: var(--color-danger);
  font-size: 12px;
  line-height: 1.35;
  margin-bottom: 6px;
  padding: 7px 8px;
  word-break: break-word;
}

.parent-row {
  margin-bottom: 2px;
}
</style>
