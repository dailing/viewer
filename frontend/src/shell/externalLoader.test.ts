import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ExternalPluginLoader, type ExternalAssetEntry } from "./externalLoader";

function asset(hash: string): ExternalAssetEntry {
  return { url: `/plugins/demo/assets/${hash}/`, entry: "frontend.js", hash };
}

describe("ExternalPluginLoader", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("retries a failed hash after cooldown without another mailbox frame", async () => {
    let attempts = 0;
    const load = vi.fn(async () => {
      attempts++;
      if (attempts === 1) throw new Error("activation failed");
      return { id: "demo" };
    });
    const loader = new ExternalPluginLoader({ load, unload: vi.fn() });
    loader.setAssets({ demo: asset("one") });
    loader.setConnected(new Set(["demo"]));
    await loader.settled();
    expect(load).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(29_999);
    await loader.settled();
    expect(load).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    await loader.settled();
    expect(load).toHaveBeenCalledTimes(2);

    loader.setAssets({ demo: asset("one") });
    await loader.settled();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("unloads on disconnect and does not retry while offline", async () => {
    const unload = vi.fn();
    const loader = new ExternalPluginLoader({
      load: vi.fn(async () => ({ id: "demo" })),
      unload,
    });
    loader.setAssets({ demo: asset("one") });
    loader.setConnected(new Set(["demo"]));
    await loader.settled();
    loader.setConnected(new Set());
    await loader.settled();
    expect(unload).toHaveBeenCalledWith({ id: "demo" });

    loader.setConnected(new Set(["demo"]));
    await loader.settled();
    expect(unload).toHaveBeenCalledTimes(1);
  });

  it("serializes overlapping mailbox updates around an async load", async () => {
    let finish: ((value: { id: string }) => void) | undefined;
    const load = vi.fn(() => new Promise<{ id: string }>((resolve) => { finish = resolve; }));
    const loader = new ExternalPluginLoader({ load, unload: vi.fn() });
    loader.setAssets({ demo: asset("one") });
    loader.setConnected(new Set(["demo"]));
    loader.setAssets({ demo: asset("one") });
    loader.setConnected(new Set(["demo"]));
    await Promise.resolve();
    expect(finish).toBeTypeOf("function");
    finish?.({ id: "demo" });
    await loader.settled();
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("unloads the old generation before activating a changed hash", async () => {
    const events: string[] = [];
    const loader = new ExternalPluginLoader({
      load: async (_id, entry) => {
        events.push(`load:${entry.hash}`);
        return { hash: entry.hash };
      },
      unload: (handle) => events.push(`unload:${handle.hash}`),
    });
    loader.setAssets({ demo: asset("one") });
    loader.setConnected(new Set(["demo"]));
    await loader.settled();
    loader.setAssets({ demo: asset("two") });
    await loader.settled();
    expect(events).toEqual(["load:one", "unload:one", "load:two"]);
  });
});
