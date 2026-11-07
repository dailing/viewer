<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, ref, watch, watchEffect } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { PaneChromeAction } from "../../stores/paneChrome";
import { basename } from "../../services/fileTypes";
import type { FileEntry } from "../../services/fileTypes";
import { fileTicketUrl } from "../../services/fileData";
import { captureDroppedItems } from "./directoryUpload";
import FileBrowser from "./FileBrowser.vue";
import UploadQueuePanel from "./UploadQueuePanel.vue";
import { getInstance, setPinned, updateState } from "./instanceStore";
import { fileUploadQueue, uploadDirectoryChangedEvent } from "./uploadQueue";
import { openInPairedViewer } from "./viewerTarget";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("FileManagerPane must be mounted inside PluginPaneHost");
const ctx = injectedCtx;
const instanceId = ctx.instanceId;
const record = computed(() => getInstance(instanceId));
const dir = ref(record.value?.state.dir ?? "");
const showHidden = ref(record.value?.state.showHidden ?? false);
const pinned = computed(() => record.value?.pinned ?? false);
const error = ref("");
const browserRef = ref<InstanceType<typeof FileBrowser> | null>(null);
const uploadInput = ref<HTMLInputElement | null>(null);
const uploadQueue = fileUploadQueue;
const uploadQueueOpen = ref(uploadQueue.summary.value.activeJobs > 0 || uploadQueue.state.scanning > 0);
const draggingFiles = ref(false);
let dragDepth = 0;
const label = computed(() => dir.value === "" ? "文件管理器" : basename(dir.value));
const uploadQueueTitle = computed(() => {
  const summary = uploadQueue.summary.value;
  if (uploadQueue.state.scanning > 0) return "上传队列：正在读取目录结构";
  if (summary.activeJobs > 0) return `上传队列：${summary.activeJobs} 项进行中，${summary.percent.toFixed(0)}%`;
  return `上传队列（${summary.totalJobs} 项）`;
});

watch(() => record.value?.state, (state) => {
  if (state === undefined) return;
  if (state.dir !== dir.value) dir.value = state.dir;
  if (state.showHidden !== showHidden.value) showHidden.value = state.showHidden;
});
watch(dir, () => updateState(instanceId, { dir: dir.value }, label.value));
watch(showHidden, () => updateState(instanceId, { showHidden: showHidden.value }));
async function openFile(entry: FileEntry): Promise<void> {
  error.value = "";
  try {
    await openInPairedViewer(ctx, instanceId, entry.path);
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "无法打开文件预览";
  }
}

async function downloadFile(entry: FileEntry): Promise<void> {
  error.value = "";
  try {
    const url = await fileTicketUrl(ctx.bus, entry.path);
    const anchor = document.createElement("a");
    anchor.href = `${url}&download=1`;
    anchor.download = entry.name;
    anchor.click();
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "下载失败";
  }
}

function handleUploads(event: Event): void {
  const input = event.target as HTMLInputElement;
  const files = [...(input.files ?? [])];
  input.value = "";
  if (files.length === 0) return;
  error.value = "";
  uploadQueueOpen.value = true;
  uploadQueue.enqueueFiles(files, dir.value, ctx.bus);
}

function hasDraggedFiles(event: DragEvent): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes("Files");
}
function handleDragEnter(event: DragEvent): void {
  if (!hasDraggedFiles(event)) return;
  event.preventDefault();
  dragDepth += 1;
  draggingFiles.value = true;
  if (event.dataTransfer !== null) event.dataTransfer.dropEffect = "copy";
}
function handleDragOver(event: DragEvent): void {
  if (!hasDraggedFiles(event)) return;
  event.preventDefault();
  if (event.dataTransfer !== null) event.dataTransfer.dropEffect = "copy";
}
function handleDragLeave(event: DragEvent): void {
  if (!draggingFiles.value) return;
  event.preventDefault();
  dragDepth = Math.max(0, dragDepth - 1);
  if (dragDepth === 0) draggingFiles.value = false;
}
function handleDrop(event: DragEvent): void {
  if (!hasDraggedFiles(event)) return;
  event.preventDefault();
  dragDepth = 0;
  draggingFiles.value = false;
  const dataTransfer = event.dataTransfer;
  if (dataTransfer === null) return;
  // Entries must be captured before the drop event returns; traversal is async.
  const items = captureDroppedItems(dataTransfer);
  if (items.length === 0) {
    error.value = "拖放内容中没有可上传的文件或文件夹";
    return;
  }
  error.value = "";
  uploadQueueOpen.value = true;
  // The shared queue fixes the destination now and survives this pane unmounting.
  void uploadQueue.scanAndEnqueue(items, dir.value, ctx.bus);
}

function handleUploadedDirectory(event: Event): void {
  const changedDir = (event as CustomEvent<{ dir?: string }>).detail?.dir;
  if (changedDir === dir.value) browserRef.value?.reload();
}
onMounted(() => window.addEventListener(uploadDirectoryChangedEvent, handleUploadedDirectory));
onBeforeUnmount(() => window.removeEventListener(uploadDirectoryChangedEvent, handleUploadedDirectory));

watchEffect(() => {
  const actions: PaneChromeAction[] = [
    {
      id: "show-hidden",
      title: showHidden.value ? "隐藏以 . 开头的文件" : "显示以 . 开头的文件",
      icon: showHidden.value ? "bi-eye" : "bi-eye-slash",
      active: showHidden.value,
      run: () => { showHidden.value = !showHidden.value; },
    },
    {
      id: "new-folder",
      title: "新建文件夹",
      icon: "bi-folder-plus",
      active: false,
      run: () => { void browserRef.value?.createFolder(); },
    },
    {
      id: "upload",
      title: "上传文件到当前目录（也可直接拖放文件或文件夹到面板）",
      icon: "bi-upload",
      active: false,
      run: () => uploadInput.value?.click(),
    },
    {
      id: "upload-queue",
      title: uploadQueueTitle.value,
      icon: "bi-cloud-upload",
      active: uploadQueueOpen.value,
      run: () => { uploadQueueOpen.value = !uploadQueueOpen.value; },
    },
    {
      id: "new-manager",
      title: "在新面板打开此目录",
      icon: "bi-columns-gap",
      active: false,
      run: () => { void ctx.shell.open({ paneType: "file-manager", params: { dir: dir.value }, newPane: true }); },
    },
    {
      id: "pin",
      title: pinned.value ? "取消固定（关闭面板后删除此实例）" : "固定实例（关闭面板后保留）",
      icon: pinned.value ? "bi-pin-angle-fill" : "bi-pin-angle",
      active: pinned.value,
      run: () => setPinned(instanceId, !pinned.value),
    },
  ];
  ctx.setChrome({ title: label.value, actions });
});
</script>

<template>
  <div
    class="file-manager-pane"
    :class="{ 'drop-active': draggingFiles }"
    @dragenter="handleDragEnter"
    @dragover="handleDragOver"
    @dragleave="handleDragLeave"
    @drop="handleDrop"
  >
    <input ref="uploadInput" class="d-none" type="file" multiple @change="handleUploads">
    <UploadQueuePanel v-if="uploadQueueOpen" @close="uploadQueueOpen = false" />
    <div v-if="draggingFiles" class="upload-drop-overlay" aria-hidden="true">
      <i class="bi bi-upload" />
      <strong>释放以上传文件或文件夹</strong>
      <span>{{ dir || "当前目录" }}</span>
    </div>
    <div v-if="error" class="small text-danger px-3 py-1">{{ error }}</div>
    <FileBrowser
      ref="browserRef"
      :dir="dir"
      :show-hidden="showHidden"
      @navigate="dir = $event"
      @open="openFile"
      @resolved="dir = $event"
      @download="downloadFile"
    />
  </div>
</template>

<style scoped>
.file-manager-pane {
  background: var(--color-surface);
  color: var(--color-text);
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 0;
  overflow: hidden;
  position: relative;
}
.upload-drop-overlay {
  align-items: center;
  background: color-mix(in srgb, var(--color-surface) 92%, var(--color-accent));
  border: 1px dashed var(--color-accent);
  color: var(--color-accent);
  display: flex;
  flex-direction: column;
  gap: 5px;
  inset: 5px;
  justify-content: center;
  pointer-events: none;
  position: absolute;
  z-index: 5;
}
.upload-drop-overlay > i { font-size: 22px; }
.upload-drop-overlay > span {
  color: var(--color-text-muted);
  font-size: var(--font-size-ui-small);
  max-width: 90%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.file-manager-pane > :last-child { flex: 1; min-height: 0; }
</style>
