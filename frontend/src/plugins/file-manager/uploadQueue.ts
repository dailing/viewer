import { computed, reactive } from "vue";
import { ensureDirectory, joinUploadPath, scanDroppedItems, type DroppedItem, type UploadTreeEntry } from "./directoryUpload";
import {
  uploadFileWithProgress,
  UploadConflictError,
  type UploadBus,
  type UploadProgress,
  type UploadResult,
} from "../../services/uploadFile";

export type UploadJobStatus = "queued" | "creating" | "uploading" | "done" | "error";
export interface UploadJob {
  id: string;
  kind: "file" | "directory";
  name: string;
  targetDir: string;
  file?: File;
  directoryName?: string;
  status: UploadJobStatus;
  message: string;
  loaded: number;
  total: number;
  startedAt?: number;
  finishedAt?: number;
}
export interface UploadSummary {
  totalJobs: number;
  activeJobs: number;
  doneJobs: number;
  errorJobs: number;
  totalBytes: number;
  progressBytes: number;
  percent: number;
  bytesPerSecond: number;
  etaSeconds: number | null;
}
export interface UploadQueueDependencies {
  upload(bus: UploadBus, dir: string, file: File, overwrite: boolean, onProgress: UploadProgress): Promise<UploadResult>;
  ensureDirectory(bus: UploadBus, dir: string, name: string): Promise<void>;
  confirmOverwrite(name: string): boolean;
  notifyDirectoryChanged(dir: string): void;
  now(): number;
}

export const uploadDirectoryChangedEvent = "viewer:file-manager-upload-directory-changed";

export function summarizeUploadJobs(jobs: UploadJob[], now: number): UploadSummary {
  const activeJobs = jobs.filter((job) => ["queued", "creating", "uploading"].includes(job.status)).length;
  const doneJobs = jobs.filter((job) => job.status === "done").length;
  const errorJobs = jobs.filter((job) => job.status === "error").length;
  const countedFiles = jobs.filter((job) => job.kind === "file" && job.status !== "error");
  const totalBytes = countedFiles.reduce((sum, job) => sum + job.total, 0);
  const progressBytes = countedFiles.reduce((sum, job) => (
    sum + (job.status === "done" ? job.total : job.status === "uploading" ? job.loaded : 0)
  ), 0);
  const current = jobs.find((job) => job.kind === "file" && job.status === "uploading");
  const elapsedSeconds = current?.startedAt === undefined ? 0 : Math.max(0, (now - current.startedAt) / 1000);
  const bytesPerSecond = current !== undefined && elapsedSeconds > 0 ? current.loaded / elapsedSeconds : 0;
  const remainingBytes = Math.max(0, totalBytes - progressBytes);
  return {
    totalJobs: jobs.length,
    activeJobs,
    doneJobs,
    errorJobs,
    totalBytes,
    progressBytes,
    percent: totalBytes > 0 ? Math.min(100, progressBytes / totalBytes * 100) : activeJobs === 0 && jobs.length > 0 ? 100 : 0,
    bytesPerSecond,
    etaSeconds: bytesPerSecond > 0 ? remainingBytes / bytesPerSecond : null,
  };
}

export function createUploadQueue(overrides: Partial<UploadQueueDependencies> = {}) {
  const deps: UploadQueueDependencies = {
    upload: uploadFileWithProgress,
    ensureDirectory,
    confirmOverwrite: (name) => window.confirm(`“${name}”已存在。是否覆盖？`),
    notifyDirectoryChanged: (dir) => {
      window.dispatchEvent(new CustomEvent(uploadDirectoryChangedEvent, { detail: { dir } }));
    },
    now: () => Date.now(),
    ...overrides,
  };
  const state = reactive({ jobs: [] as UploadJob[], scanning: 0, error: "" });
  const summary = computed(() => summarizeUploadJobs(state.jobs, deps.now()));
  const pending: Array<{ job: UploadJob; bus: UploadBus }> = [];
  let nextJobId = 0;
  let draining = false;
  let scanChain: Promise<void> = Promise.resolve();

  async function uploadOne(job: UploadJob, bus: UploadBus): Promise<void> {
    const file = job.file;
    if (file === undefined) throw new Error("上传任务缺少文件");
    const report: UploadProgress = (loaded, total) => {
      job.loaded = Math.max(0, Math.min(loaded, total));
      job.total = total > 0 ? total : file.size;
    };
    job.startedAt = deps.now();
    try {
      await deps.upload(bus, job.targetDir, file, false, report);
    } catch (cause) {
      if (!(cause instanceof UploadConflictError)) throw cause;
      job.loaded = 0;
      job.message = "等待覆盖确认";
      // Confirmation precedes the overwrite-authorized ticket.
      if (!deps.confirmOverwrite(file.name)) throw new Error("已取消覆盖");
      job.startedAt = deps.now();
      job.message = "上传中";
      await deps.upload(bus, job.targetDir, file, true, report);
    }
  }

  async function runJob(job: UploadJob, bus: UploadBus): Promise<void> {
    job.startedAt = deps.now();
    job.loaded = 0;
    try {
      if (job.kind === "file") {
        job.status = "uploading";
        job.message = "上传中";
        await uploadOne(job, bus);
      } else {
        job.status = "creating";
        job.message = "创建目录中";
        await deps.ensureDirectory(bus, job.targetDir, job.directoryName ?? job.name);
      }
      job.loaded = job.total;
      job.status = "done";
      job.message = job.kind === "file" ? "已上传" : "已创建";
      job.finishedAt = deps.now();
      deps.notifyDirectoryChanged(job.targetDir);
    } catch (cause) {
      job.status = "error";
      job.message = cause instanceof Error ? cause.message : job.kind === "file" ? "上传失败" : "创建目录失败";
      job.finishedAt = deps.now();
    }
  }

  async function drain(): Promise<void> {
    if (draining) return;
    draining = true;
    try {
      // A single worker preserves directory-before-child order and serializes prompts.
      for (let next = pending.shift(); next !== undefined; next = pending.shift()) {
        await runJob(next.job, next.bus);
      }
    } finally {
      draining = false;
    }
  }

  function enqueueTree(items: UploadTreeEntry[], targetRoot: string, bus: UploadBus): void {
    if (items.length === 0) return;
    state.error = "";
    for (const item of items) {
      const segments = item.path.split("/");
      const name = segments.pop() ?? item.path;
      const targetDir = segments.reduce((parent, segment) => joinUploadPath(parent, segment), targetRoot);
      const job = reactive({
        id: `upload:${nextJobId++}`,
        kind: item.kind,
        name: item.kind === "directory" ? `${item.path}/` : item.path,
        targetDir,
        file: item.kind === "file" ? item.file : undefined,
        directoryName: item.kind === "directory" ? name : undefined,
        status: "queued",
        message: "等待上传",
        loaded: 0,
        total: item.kind === "file" ? item.file.size : 0,
      }) as UploadJob;
      state.jobs.push(job);
      pending.push({ job, bus });
    }
    void drain();
  }

  function enqueueFiles(files: File[], targetDir: string, bus: UploadBus): void {
    enqueueTree(files.map((file) => ({ kind: "file", path: file.name, file })), targetDir, bus);
  }

  function scanAndEnqueue(items: DroppedItem[], targetRoot: string, bus: UploadBus): Promise<void> {
    state.scanning += 1;
    state.error = "";
    // Preserve drop-batch order even when a large directory takes longer to scan.
    scanChain = scanChain.then(async () => {
      const scanned = await scanDroppedItems(items);
      // Navigation during scanning cannot change this batch's destination.
      enqueueTree(scanned, targetRoot, bus);
    }).catch((cause) => {
      state.error = `读取拖放目录失败：${cause instanceof Error ? cause.message : "未知错误"}`;
    }).finally(() => {
      state.scanning -= 1;
    });
    return scanChain;
  }

  function clearFinished(): void {
    state.jobs = state.jobs.filter((job) => job.status !== "done" && job.status !== "error");
    state.error = "";
  }

  return { state, summary, enqueueFiles, enqueueTree, scanAndEnqueue, clearFinished };
}

/** Browser-tab singleton: all FileManagerPane instances render this same queue. */
export const fileUploadQueue = createUploadQueue();
