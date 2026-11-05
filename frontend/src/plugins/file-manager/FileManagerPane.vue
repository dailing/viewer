<script setup lang="ts">
import { computed, inject, ref, watch, watchEffect } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { PaneChromeAction } from "../../stores/paneChrome";
import { basename } from "../../services/fileTypes";
import type { FileEntry } from "../../services/fileTypes";
import { fileTicketUrl } from "../../services/fileData";
import { uploadFile, UploadConflictError } from "../../services/uploadFile";
import FileBrowser from "./FileBrowser.vue";
import { getInstance, setPinned, updateState } from "./instanceStore";
import { openInPairedViewer } from "./viewerTarget";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("FileManagerPane must be mounted inside PluginPaneHost");
const ctx = injectedCtx;
const instanceId = ctx.instanceId;
const record = computed(() => getInstance(instanceId));
const dir = ref(record.value?.state.dir ?? "");
const pinned = computed(() => record.value?.pinned ?? false);
const error = ref("");
const browserRef = ref<InstanceType<typeof FileBrowser> | null>(null);
const uploadInput = ref<HTMLInputElement | null>(null);
interface UploadStatus {
  key: string;
  name: string;
  status: "uploading" | "done" | "error";
  message: string;
}
const uploads = ref<UploadStatus[]>([]);
const label = computed(() => dir.value === "" ? "文件管理器" : basename(dir.value));

watch(() => record.value?.state, (state) => {
  if (state !== undefined && state.dir !== dir.value) dir.value = state.dir;
});
watch(dir, () => updateState(instanceId, { dir: dir.value }, label.value));
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

async function uploadOne(file: File, status: UploadStatus): Promise<void> {
  try {
    try {
      await uploadFile(ctx.bus, dir.value, file);
    } catch (cause) {
      if (!(cause instanceof UploadConflictError)) throw cause;
      if (!confirm(`“${file.name}”已存在。是否覆盖？`)) throw new Error("已取消覆盖");
      // Confirmation happens before the overwrite-authorized ticket exists.
      await uploadFile(ctx.bus, dir.value, file, true);
    }
    status.status = "done";
    status.message = "已上传";
    browserRef.value?.reload();
  } catch (cause) {
    status.status = "error";
    status.message = cause instanceof Error ? cause.message : "上传失败";
  }
}

async function handleUploads(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const files = [...(input.files ?? [])];
  input.value = "";
  if (files.length === 0) return;
  error.value = "";
  uploads.value = files.map((file, index) => ({
    key: `${Date.now()}:${index}`,
    name: file.name,
    status: "uploading",
    message: "上传中",
  }));
  // Sequential uploads keep collision confirmations and per-file status clear.
  for (let index = 0; index < files.length; index += 1) {
    await uploadOne(files[index], uploads.value[index]);
  }
}

watchEffect(() => {
  const actions: PaneChromeAction[] = [
    {
      id: "upload",
      title: "上传文件到当前目录",
      icon: "bi-upload",
      active: false,
      run: () => uploadInput.value?.click(),
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
  <div class="file-manager-pane">
    <input ref="uploadInput" class="d-none" type="file" multiple @change="handleUploads">
    <div v-if="error" class="small text-danger px-3 py-1">{{ error }}</div>
    <div v-if="uploads.length > 0" class="upload-statuses">
      <div class="upload-status-heading">
        <span>上传</span>
        <button type="button" class="upload-clear" title="清除上传状态" aria-label="清除上传状态" @click="uploads = []">
          <i class="bi bi-x-lg" />
        </button>
      </div>
      <div v-for="upload in uploads" :key="upload.key" class="upload-status" :title="upload.message">
        <i class="bi" :class="upload.status === 'done' ? 'bi-check2' : upload.status === 'error' ? 'bi-exclamation-circle' : 'bi-arrow-repeat'" />
        <span class="upload-name">{{ upload.name }}</span>
        <span :class="{ 'text-danger': upload.status === 'error' }">{{ upload.message }}</span>
      </div>
    </div>
    <FileBrowser
      ref="browserRef"
      :dir="dir"
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
}
.file-manager-pane > :last-child { flex: 1; min-height: 0; }
.upload-statuses {
  border-bottom: 1px solid var(--color-border);
  color: var(--color-text-subtle);
  flex: 0 0 auto;
  font-size: var(--font-size-ui-small);
  max-height: 120px;
  overflow-y: auto;
  padding: 3px 10px;
}
.upload-status-heading,
.upload-status {
  align-items: center;
  display: flex;
  gap: 7px;
  min-height: 20px;
}
.upload-status-heading { color: var(--color-text-muted); }
.upload-clear {
  background: transparent;
  border: 0;
  color: inherit;
  margin-left: auto;
  padding: 0 3px;
}
.upload-name {
  color: var(--color-text);
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
