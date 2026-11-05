import { reactive } from "vue";

export interface DirectorySizeResult {
  path: string;
  size: number;
  files: number;
  directories: number;
  errors: number;
  complete: boolean;
}
export interface DirectorySizeState {
  status: "idle" | "loading" | "ready" | "error";
  result?: DirectorySizeResult;
  error?: string;
}
interface SizeRequestBus {
  request(channel: string, value: unknown, options?: { timeout?: number }): Promise<unknown>;
}

/** Per-directory, explicitly triggered recursive sizing. Listing never calls it. */
export class DirectorySizes {
  readonly states = reactive(new Map<string, DirectorySizeState>());
  private pending = new Map<string, Promise<void>>();

  state(path: string): DirectorySizeState {
    return this.states.get(path) ?? { status: "idle" };
  }

  calculate(bus: SizeRequestBus, path: string): Promise<void> {
    const existing = this.pending.get(path);
    if (existing !== undefined) return existing;
    const request = this.run(bus, path).finally(() => this.pending.delete(path));
    this.pending.set(path, request);
    return request;
  }

  private async run(bus: SizeRequestBus, path: string): Promise<void> {
    this.states.set(path, { status: "loading" });
    try {
      const result = await bus.request("file:_:du", { path }, { timeout: 120_000 }) as DirectorySizeResult;
      this.states.set(path, { status: "ready", result });
    } catch (cause) {
      this.states.set(path, { status: "error", error: cause instanceof Error ? cause.message : "目录大小计算失败" });
    }
  }
}

export const directorySizes = new DirectorySizes();
