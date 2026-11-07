import { reactive } from "vue";

export interface DirectorySizeResult {
  path: string;
  size: number;
  files: number;
  directories: number;
  errors: number;
  complete: boolean;
}
export interface DirectorySizeProgress extends DirectorySizeResult {
  done: boolean;
  cancelled?: boolean;
  error?: string;
}
export interface DirectorySizeState {
  status: "idle" | "queued" | "loading" | "ready" | "error";
  result?: DirectorySizeResult;
  error?: string;
}
interface SizeRequestBus {
  request(channel: string, value: unknown, options?: { timeout?: number }): Promise<unknown>;
  subscribe(channel: string, handler: (frame: { value: unknown }) => void): void | Promise<void>;
  onStateChange?(handler: (connected: boolean) => void): void;
}
interface PendingSize {
  promise: Promise<void>;
  finish: () => void;
}

/** Per-directory background sizing. Listing only enqueues visible directories. */
export class DirectorySizes {
  readonly states = reactive(new Map<string, DirectorySizeState>());
  private pending = new Map<string, PendingSize>();
  private subscribedBus: SizeRequestBus | null = null;

  state(path: string): DirectorySizeState {
    return this.states.get(path) ?? { status: "idle" };
  }

  ensure(bus: SizeRequestBus, path: string): Promise<void> {
    if (this.state(path).status !== "idle") return Promise.resolve();
    return this.calculate(bus, path);
  }

  calculate(bus: SizeRequestBus, path: string): Promise<void> {
    const existing = this.pending.get(path);
    if (existing !== undefined) return existing.promise;
    let finish = (): void => undefined;
    const promise = new Promise<void>((resolve) => { finish = resolve; });
    this.pending.set(path, { promise, finish });
    this.states.set(path, { status: "queued" });
    void this.start(bus, path);
    return promise;
  }

  async cancel(bus: SizeRequestBus, path: string): Promise<void> {
    if (!this.pending.has(path)) return;
    try {
      await bus.request("file:_:du:cancel", { path }, { timeout: 10_000 });
    } catch (cause) {
      this.fail(path, cause instanceof Error ? cause.message : "取消失败");
    }
  }

  private async start(bus: SizeRequestBus, path: string): Promise<void> {
    try {
      await this.subscribe(bus);
      await bus.request("file:_:du", { path }, { timeout: 10_000 });
    } catch (cause) {
      this.fail(path, cause instanceof Error ? cause.message : "目录大小计算失败");
    }
  }

  private async subscribe(bus: SizeRequestBus): Promise<void> {
    if (this.subscribedBus === bus) return;
    await bus.subscribe("file:_:du:progress", (frame) => this.apply(frame.value as DirectorySizeProgress));
    this.subscribedBus = bus;
    bus.onStateChange?.((connected) => {
      if (connected) return;
      for (const path of this.pending.keys()) this.fail(path, "连接中断，请点击重试");
    });
  }

  private apply(progress: DirectorySizeProgress): void {
    if (progress === null || typeof progress !== "object" || typeof progress.path !== "string") return;
    const pending = this.pending.get(progress.path);
    if (pending === undefined) return;
    const result: DirectorySizeResult = {
      path: progress.path,
      size: progress.size,
      files: progress.files,
      directories: progress.directories,
      errors: progress.errors,
      complete: progress.complete,
    };
    if (!progress.done) {
      this.states.set(progress.path, { status: "loading", result });
      return;
    }
    this.pending.delete(progress.path);
    if (progress.cancelled === true) {
      this.states.set(progress.path, { status: "idle" });
    } else if (progress.error !== undefined && progress.error !== "") {
      this.states.set(progress.path, { status: "error", result, error: progress.error });
    } else {
      this.states.set(progress.path, { status: "ready", result });
    }
    pending.finish();
  }

  private fail(path: string, error: string): void {
    const pending = this.pending.get(path);
    if (pending === undefined) return;
    this.pending.delete(path);
    this.states.set(path, { status: "error", result: this.states.get(path)?.result, error });
    pending.finish();
  }
}

export const directorySizes = new DirectorySizes();
