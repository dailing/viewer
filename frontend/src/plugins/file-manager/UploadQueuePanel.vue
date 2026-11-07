<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { formatBytes } from "../../services/formatBytes";
import { fileUploadQueue, summarizeUploadJobs, type UploadJob } from "./uploadQueue";

const emit = defineEmits<{ close: [] }>();
const now = ref(Date.now());
let clock: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  clock = setInterval(() => { now.value = Date.now(); }, 1000);
});
onUnmounted(() => {
  if (clock !== undefined) clearInterval(clock);
});
const summary = computed(() => summarizeUploadJobs(fileUploadQueue.state.jobs, now.value));
const hasFinished = computed(() => summary.value.doneJobs + summary.value.errorJobs > 0);

function formatEta(seconds: number | null): string {
  if (seconds === null || !Number.isFinite(seconds)) return "—";
  const value = Math.max(0, Math.ceil(seconds));
  if (value < 60) return `${value} 秒`;
  const minutes = Math.floor(value / 60);
  if (minutes < 60) return `${minutes} 分 ${value % 60} 秒`;
  return `${Math.floor(minutes / 60)} 小时 ${minutes % 60} 分`;
}
function jobPercent(job: UploadJob): number {
  if (job.kind === "directory") return job.status === "done" ? 100 : 0;
  if (job.total <= 0) return job.status === "done" ? 100 : 0;
  return Math.min(100, job.loaded / job.total * 100);
}
function jobSpeed(job: UploadJob): string {
  if (job.status !== "uploading" || job.startedAt === undefined) return "";
  const elapsed = (now.value - job.startedAt) / 1000;
  return elapsed > 0 && job.loaded > 0 ? `${formatBytes(job.loaded / elapsed)}/s` : "";
}
function jobIcon(job: UploadJob): string {
  if (job.status === "done") return "bi-check2";
  if (job.status === "error") return "bi-exclamation-circle";
  if (job.status === "queued") return "bi-clock";
  return job.kind === "directory" ? "bi-folder-plus" : "bi-arrow-repeat";
}
</script>

<template>
  <section class="upload-queue-panel" role="dialog" aria-label="上传队列">
    <header class="upload-queue-header">
      <i class="bi bi-cloud-upload" />
      <strong>上传队列</strong>
      <span v-if="summary.activeJobs > 0" class="text-muted">{{ summary.activeJobs }} 项进行中</span>
      <button type="button" title="清除已结束任务" aria-label="清除已结束任务" :disabled="!hasFinished" @click="fileUploadQueue.clearFinished()">
        <i class="bi bi-trash3" />
      </button>
      <button type="button" title="收起上传队列（不中断上传）" aria-label="收起上传队列" @click="emit('close')">
        <i class="bi bi-x-lg" />
      </button>
    </header>
    <div v-if="fileUploadQueue.state.scanning > 0" class="upload-scanning">
      <i class="bi bi-arrow-repeat" /> 正在读取拖放的目录结构…
    </div>
    <div v-if="fileUploadQueue.state.error" class="upload-scan-error">{{ fileUploadQueue.state.error }}</div>
    <div v-if="summary.totalJobs > 0" class="upload-overall">
      <div class="upload-overall-line">
        <span>{{ summary.doneJobs }}/{{ summary.totalJobs }} 完成</span>
        <span v-if="summary.errorJobs > 0" class="text-danger">{{ summary.errorJobs }} 失败</span>
        <span>{{ summary.percent.toFixed(0) }}%</span>
      </div>
      <div class="upload-progress-track">
        <div class="upload-progress-value" :style="{ width: `${summary.percent}%` }" />
      </div>
      <div class="upload-overall-line upload-metrics">
        <span>{{ formatBytes(summary.progressBytes) }} / {{ formatBytes(summary.totalBytes) }}</span>
        <span>速度 {{ summary.bytesPerSecond > 0 ? `${formatBytes(summary.bytesPerSecond)}/s` : "—" }}</span>
        <span>ETA {{ formatEta(summary.etaSeconds) }}</span>
      </div>
    </div>
    <div v-if="fileUploadQueue.state.jobs.length === 0" class="upload-empty">
      {{ fileUploadQueue.state.scanning > 0 ? "扫描完成后将进入上传队列" : "暂无上传任务" }}
    </div>
    <div v-else class="upload-job-list">
      <div v-for="job in fileUploadQueue.state.jobs" :key="job.id" class="upload-job" :class="{ error: job.status === 'error' }">
        <div class="upload-job-line">
          <i class="bi" :class="jobIcon(job)" />
          <span class="upload-job-name" :title="`${job.name} → ${job.targetDir}`">{{ job.name }}</span>
          <span class="upload-job-message">{{ job.message }}</span>
        </div>
        <div v-if="job.kind === 'file' && (job.status === 'uploading' || job.status === 'done')" class="upload-job-line upload-job-detail">
          <span>{{ formatBytes(job.loaded) }} / {{ formatBytes(job.total) }}</span>
          <span>{{ jobPercent(job).toFixed(0) }}%</span>
          <span>{{ jobSpeed(job) }}</span>
        </div>
        <div v-if="job.status === 'uploading'" class="upload-progress-track small">
          <div class="upload-progress-value" :style="{ width: `${jobPercent(job)}%` }" />
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.upload-queue-panel {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  box-shadow: 0 8px 28px rgb(0 0 0 / 24%);
  color: var(--color-text);
  display: flex;
  flex-direction: column;
  font-size: var(--font-size-ui-small);
  max-height: calc(100% - 16px);
  position: absolute;
  right: 8px;
  top: 8px;
  width: min(400px, calc(100% - 16px));
  z-index: 20;
}
.upload-queue-header {
  align-items: center;
  border-bottom: 1px solid var(--color-border);
  display: flex;
  flex: 0 0 auto;
  gap: 7px;
  min-height: 32px;
  padding: 3px 7px 3px 10px;
}
.upload-queue-header > span { flex: 1; }
.upload-queue-header > strong { white-space: nowrap; }
.upload-queue-header > span:first-of-type { margin-left: auto; }
.upload-queue-header button {
  background: transparent;
  border: 0;
  color: var(--color-text-muted);
  padding: 3px 5px;
}
.upload-queue-header button:hover:not(:disabled) { color: var(--color-text); }
.upload-queue-header button:disabled { opacity: 0.35; }
.upload-scanning, .upload-scan-error, .upload-empty { flex: 0 0 auto; padding: 7px 10px; }
.upload-scan-error { color: var(--color-danger); }
.upload-overall { border-bottom: 1px solid var(--color-border); flex: 0 0 auto; padding: 7px 10px; }
.upload-overall-line { display: flex; gap: 10px; justify-content: space-between; }
.upload-metrics { color: var(--color-text-muted); margin-top: 5px; }
.upload-progress-track {
  background: var(--color-surface-alt);
  border-radius: 2px;
  height: 4px;
  margin-top: 6px;
  overflow: hidden;
}
.upload-progress-track.small { height: 2px; margin: 3px 0 0 19px; }
.upload-progress-value { background: var(--color-accent); height: 100%; transition: width 120ms linear; }
.upload-job-list { min-height: 0; overflow-y: auto; padding: 3px 0; }
.upload-job { border-bottom: 1px solid color-mix(in srgb, var(--color-border) 55%, transparent); padding: 5px 10px; }
.upload-job:last-child { border-bottom: 0; }
.upload-job-line { align-items: center; display: flex; gap: 7px; min-height: 18px; }
.upload-job-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.upload-job-message { color: var(--color-text-muted); flex: 0 0 auto; max-width: 45%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.upload-job.error .upload-job-message { color: var(--color-danger); }
.upload-job-detail { color: var(--color-text-muted); justify-content: flex-end; padding-left: 19px; }
</style>
