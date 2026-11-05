import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

import { closeShellPane, focusShellPane, listShellPanes, moveShellPane } from "./panes";
import { useLayoutStore } from "../stores/layout";

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

describe("Shell pane API", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", storageMock());
    setActivePinia(createPinia());
  });

  it("lists tiled and floating panes with their instance identity", () => {
    const layout = useLayoutStore();
    const first = layout.openInstance("chat", "view-1");
    const second = layout.openInstance("file-viewer", "viewer-1");
    layout.floatPane(second);
    expect(listShellPanes()).toEqual([
      { paneId: first, paneType: "chat", instanceId: "view-1", location: "floating", active: false },
      { paneId: expect.any(String), paneType: "chat", instanceId: "view-1", location: "tiled", active: true },
    ].slice(1).concat({
      paneId: expect.any(String), paneType: "file-viewer", instanceId: "viewer-1", location: "floating", active: false,
    }));
  });

  it("focuses, moves and closes through the shared layout operations", () => {
    const layout = useLayoutStore();
    const first = layout.openInstance("chat", "view-1");
    const second = layout.openInstance("file-viewer", "viewer-1");
    focusShellPane(first);
    expect(layout.activePaneId).toBe(first);
    moveShellPane(first, 1);
    expect(layout.activePaneId).toBe(second);
    expect(layout.panes.map((pane) => pane.content?.instanceId)).toEqual(["viewer-1", "view-1"]);
    closeShellPane(second);
    expect(listShellPanes()).toHaveLength(1);
  });

  it("raises and closes floating panes", () => {
    const layout = useLayoutStore();
    const first = layout.openInstance("chat", "view-1");
    const second = layout.openInstance("file-viewer", "viewer-1");
    layout.floatPane(first);
    layout.floatPane(second);
    const floating = layout.floating[0];
    expect(floating).toBeDefined();
    const oldZ = floating?.z;
    focusShellPane(floating?.id ?? "");
    expect(floating?.z).toBeGreaterThan(oldZ ?? 0);
    closeShellPane(floating?.id ?? "");
    expect(layout.floating).toHaveLength(1);
  });
});
