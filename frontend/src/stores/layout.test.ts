import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { nextTick } from "vue";

import {
  startLayoutPersistence,
  useLayoutStore,
  WORKSPACE_STORAGE_KEY,
} from "./layout";

function storageMock(): Storage {
  const values = new Map<string, string>();
  return {
    get length() { return values.size; },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => { values.delete(key); },
    setItem: (key, value) => { values.set(key, value); },
  };
}

describe("Layout workspace persistence", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", storageMock());
    setActivePinia(createPinia());
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("restores split, active and floating panes while normalizing transient state", () => {
    localStorage.setItem(WORKSPACE_STORAGE_KEY, JSON.stringify({
      root: {
        type: "split",
        id: "p2",
        direction: "horizontal",
        ratio: 2,
        first: { type: "pane", id: "p1", content: { paneType: "chat", instanceId: "view-1" }, epoch: 8 },
        second: { type: "pane", id: "p3", content: { paneType: "file-viewer", instanceId: "viewer-1" }, epoch: 9 },
      },
      activePaneId: "p3",
      floating: [{
        id: "p4",
        content: { paneType: "terminal", instanceId: "term-1" },
        x: 10.6,
        y: -4.6,
        w: 100,
        h: 100,
        z: 7,
        epoch: 10,
      }],
    }));

    const layout = useLayoutStore();
    expect(layout.root).toMatchObject({ type: "split", ratio: 0.85 });
    expect(layout.panes.map((pane) => ({ id: pane.id, content: pane.content, epoch: pane.epoch }))).toEqual([
      { id: "p1", content: { paneType: "chat", instanceId: "view-1" }, epoch: 0 },
      { id: "p3", content: { paneType: "file-viewer", instanceId: "viewer-1" }, epoch: 0 },
    ]);
    expect(layout.activePaneId).toBe("p3");
    expect(layout.floating).toEqual([{
      id: "p4",
      content: { paneType: "terminal", instanceId: "term-1" },
      x: 11,
      y: -5,
      w: 240,
      h: 160,
      z: 7,
      epoch: 0,
    }]);
    expect(layout.newId()).toBe("p5");
    expect(layout.nextZ).toBe(8);
  });

  it("falls back to an empty workspace for malformed or inconsistent snapshots", () => {
    const invalid = [
      "{",
      JSON.stringify({ root: { type: "pane", id: "p1", content: null }, activePaneId: "missing", floating: [] }),
      JSON.stringify({
        root: { type: "pane", id: "p1", content: { paneType: "chat", instanceId: "same" } },
        activePaneId: "p1",
        floating: [{ id: "p2", content: { paneType: "chat", instanceId: "same" }, x: 0, y: 0, w: 300, h: 200, z: 1 }],
      }),
    ];
    for (const raw of invalid) {
      localStorage.setItem(WORKSPACE_STORAGE_KEY, raw);
      setActivePinia(createPinia());
      const layout = useLayoutStore();
      expect(layout.root).toEqual({ type: "pane", id: "p1", content: null, epoch: 0 });
      expect(layout.activePaneId).toBe("p1");
      expect(layout.floating).toEqual([]);
    }
  });

  it("debounces mutations and flushes the latest active pane on pagehide", async () => {
    vi.useFakeTimers();
    const listeners = new Map<string, () => void>();
    vi.stubGlobal("window", {
      addEventListener: (type: string, listener: () => void) => listeners.set(type, listener),
      removeEventListener: (type: string) => listeners.delete(type),
    });
    const layout = useLayoutStore();
    const stop = startLayoutPersistence();
    const first = layout.openInstance("chat", "view-1");
    layout.openInstance("file-viewer", "viewer-1");
    const floated = layout.openInstance("terminal", "term-1");
    layout.floatPane(floated);
    await nextTick();
    await vi.advanceTimersByTimeAsync(200);

    let saved = JSON.parse(localStorage.getItem(WORKSPACE_STORAGE_KEY) ?? "{}");
    expect(saved.root.type).toBe("split");
    expect(saved.floating[0]).toMatchObject({ id: "p6", content: { paneType: "terminal", instanceId: "term-1" } });

    layout.setActivePane(first);
    await nextTick();
    listeners.get("pagehide")?.();
    saved = JSON.parse(localStorage.getItem(WORKSPACE_STORAGE_KEY) ?? "{}");
    expect(saved.activePaneId).toBe(first);
    stop();
  });

  it("persists an explicitly emptied workspace", async () => {
    vi.useFakeTimers();
    const layout = useLayoutStore();
    const stop = startLayoutPersistence();
    layout.openInstance("chat", "view-1");
    layout.closePane("p1");
    await nextTick();
    await vi.advanceTimersByTimeAsync(200);
    const saved = JSON.parse(localStorage.getItem(WORKSPACE_STORAGE_KEY) ?? "{}");
    expect(saved.root).toMatchObject({ type: "pane", id: "p1", content: null });
    stop();
  });
});
