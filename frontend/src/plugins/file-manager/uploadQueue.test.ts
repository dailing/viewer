import { describe, expect, it, vi } from "vitest";
import { UploadConflictError, type UploadResult } from "../../services/uploadFile";
import { createUploadQueue, summarizeUploadJobs, type UploadJob } from "./uploadQueue";

const result: UploadResult = { path: "/uploaded", size: 1 };
const bus = { request: vi.fn() };

describe("file manager shared upload queue", () => {
  it("shares sequential jobs across enqueue callers and retains fixed targets", async () => {
    let finishFirst!: (value: UploadResult) => void;
    const firstPending = new Promise<UploadResult>((resolve) => { finishFirst = resolve; });
    const upload = vi.fn((_bus: any, _dir: string, file: File, _overwrite: boolean, report: (loaded: number, total: number) => void) => {
      report(Math.floor(file.size / 2), file.size);
      return file.name === "a.bin" ? firstPending : Promise.resolve(result);
    });
    const queue = createUploadQueue({ upload, now: () => 1000, notifyDirectoryChanged: vi.fn() });
    queue.enqueueFiles([new File(["1234567890"], "a.bin")], "/first", bus);
    await vi.waitFor(() => expect(queue.state.jobs[0]?.status).toBe("uploading"));
    queue.enqueueFiles([new File(["xy"], "b.bin")], "/second", bus);

    expect(queue.state.jobs).toHaveLength(2);
    expect(queue.state.jobs[0]).toMatchObject({ name: "a.bin", targetDir: "/first", loaded: 5, total: 10 });
    expect(queue.state.jobs[1]).toMatchObject({ name: "b.bin", targetDir: "/second", status: "queued" });
    expect(upload).toHaveBeenCalledTimes(1);

    finishFirst(result);
    await vi.waitFor(() => expect(queue.state.jobs.every((job) => job.status === "done")).toBe(true));
    expect(upload.mock.calls.map((call) => [call[1], call[2].name])).toEqual([["/first", "a.bin"], ["/second", "b.bin"]]);
    expect(queue.summary.value).toMatchObject({ totalJobs: 2, doneJobs: 2, progressBytes: 12, totalBytes: 12, percent: 100 });
  });

  it("keeps directories before children and creates empty directories", async () => {
    const ensureDirectory = vi.fn(async (_bus: any, _dir: string, _name: string) => {});
    const upload = vi.fn(async (_bus: any, _dir: string, _file: File) => result);
    const queue = createUploadQueue({ ensureDirectory, upload, notifyDirectoryChanged: vi.fn() });
    queue.enqueueTree([
      { kind: "directory", path: "root" },
      { kind: "file", path: "root/nested/a.txt", file: new File(["a"], "a.txt") },
      { kind: "directory", path: "root/empty" },
    ], "/base", bus);
    await vi.waitFor(() => expect(queue.state.jobs.every((job) => job.status === "done")).toBe(true));
    expect(ensureDirectory.mock.calls.map((call) => [call[1], call[2]])).toEqual([
      ["/base", "root"],
      ["/base/root", "empty"],
    ]);
    expect(upload.mock.calls[0]?.[1]).toBe("/base/root/nested");
  });

  it("confirms before authorizing an overwrite retry", async () => {
    const upload = vi.fn(async (_bus: any, _dir: string, _file: File, overwrite: boolean) => {
      if (!overwrite) throw new UploadConflictError("a.txt");
      return result;
    });
    const confirmOverwrite = vi.fn(() => true);
    const queue = createUploadQueue({ upload, confirmOverwrite, notifyDirectoryChanged: vi.fn() });
    queue.enqueueFiles([new File(["new"], "a.txt")], "/base", bus);
    await vi.waitFor(() => expect(queue.state.jobs[0]?.status).toBe("done"));
    expect(confirmOverwrite).toHaveBeenCalledWith("a.txt");
    expect(upload.mock.calls.map((call) => call[3])).toEqual([false, true]);
  });

  it("computes aggregate progress, current average speed and remaining ETA", () => {
    const jobs = [
      { kind: "file", status: "done", loaded: 100, total: 100 },
      { kind: "file", status: "uploading", loaded: 50, total: 100, startedAt: 3000 },
      { kind: "file", status: "queued", loaded: 0, total: 50 },
    ] as UploadJob[];
    expect(summarizeUploadJobs(jobs, 5000)).toMatchObject({
      activeJobs: 2,
      doneJobs: 1,
      totalBytes: 250,
      progressBytes: 150,
      percent: 60,
      bytesPerSecond: 25,
      etaSeconds: 4,
    });
  });

  it("clears only settled history", async () => {
    const queue = createUploadQueue({ upload: async () => result, notifyDirectoryChanged: vi.fn() });
    queue.enqueueFiles([new File(["a"], "a.txt")], "/base", bus);
    await vi.waitFor(() => expect(queue.state.jobs[0]?.status).toBe("done"));
    queue.state.jobs.push({ kind: "file", name: "pending", status: "queued" } as UploadJob);
    queue.clearFinished();
    expect(queue.state.jobs.map((job) => job.name)).toEqual(["pending"]);
  });
});
