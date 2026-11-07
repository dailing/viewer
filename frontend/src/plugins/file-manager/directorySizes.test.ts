import { describe, expect, it, vi } from "vitest";
import { DirectorySizes, type DirectorySizeProgress } from "./directorySizes";

const result = { path: "/a", size: 42, files: 2, directories: 1, errors: 0, complete: true };
function fakeBus() {
  let progressHandler: (frame: { value: unknown }) => void = () => undefined;
  const request = vi.fn(async () => ({ status: "queued" }));
  const subscribe = vi.fn(async (_channel: string, handler: (frame: { value: unknown }) => void) => {
    progressHandler = handler;
  });
  return {
    request,
    subscribe,
    emit(value: DirectorySizeProgress) { progressHandler({ value }); },
  };
}

describe("DirectorySizes", () => {
  it("deduplicates requests and applies incremental progress before completion", async () => {
    const bus = fakeBus();
    const sizes = new DirectorySizes();
    expect(sizes.state("/a").status).toBe("idle");
    const first = sizes.calculate(bus, "/a");
    const second = sizes.calculate(bus, "/a");
    await vi.waitFor(() => expect(bus.request).toHaveBeenCalledOnce());
    expect(bus.request).toHaveBeenCalledWith("file:_:du", { path: "/a" }, { timeout: 10_000 });
    expect(sizes.state("/a").status).toBe("queued");
    bus.emit({ ...result, size: 10, complete: false, done: false });
    expect(sizes.state("/a")).toMatchObject({ status: "loading", result: { size: 10 } });
    bus.emit({ ...result, done: true });
    await Promise.all([first, second]);
    expect(sizes.state("/a")).toEqual({ status: "ready", result });
    await sizes.ensure(bus, "/a");
    expect(bus.request).toHaveBeenCalledOnce();
  });

  it("keeps partial errors and cancellation per directory", async () => {
    const bus = fakeBus();
    const sizes = new DirectorySizes();
    const partial = sizes.calculate(bus, "/partial");
    await vi.waitFor(() => expect(bus.request).toHaveBeenCalledOnce());
    bus.emit({ ...result, path: "/partial", errors: 2, complete: false, done: true });
    await partial;
    expect(sizes.state("/partial").result?.complete).toBe(false);

    const cancelled = sizes.calculate(bus, "/a");
    await vi.waitFor(() => expect(bus.request).toHaveBeenCalledTimes(2));
    await sizes.cancel(bus, "/a");
    bus.emit({ ...result, path: "/a", done: true, cancelled: true });
    await cancelled;
    expect(sizes.state("/a").status).toBe("idle");
    expect(sizes.state("/other").status).toBe("idle");
  });

  it("surfaces queue and worker failures", async () => {
    const sizes = new DirectorySizes();
    const rejected = { ...fakeBus(), request: vi.fn(async () => { throw new Error("denied"); }) };
    await sizes.calculate(rejected, "/error");
    expect(sizes.state("/error")).toMatchObject({ status: "error", error: "denied" });

    const bus = fakeBus();
    const running = sizes.calculate(bus, "/worker-error");
    await vi.waitFor(() => expect(bus.request).toHaveBeenCalledOnce());
    bus.emit({ ...result, path: "/worker-error", done: true, complete: false, error: "read failed" });
    await running;
    expect(sizes.state("/worker-error")).toMatchObject({ status: "error", error: "read failed" });
  });
});
