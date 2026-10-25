import { describe, expect, it } from "vitest";
import { BRANCH_TABS_STORAGE_KEY, WINDOW_ID_STORAGE_KEY, ensureWindowId, loadBranchTabs, writeBranchTabs, type KeyValueStorage } from "./branchTabs";

function storage(): KeyValueStorage & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    getItem: (key) => data.get(key) ?? null,
    setItem: (key, value) => { data.set(key, value); },
  };
}

describe("branch tab storage", () => {
  it("creates and reuses a per-window id", () => {
    const store = storage();
    expect(ensureWindowId(store, () => "window-1")).toBe("window-1");
    expect(ensureWindowId(store, () => "window-2")).toBe("window-1");
    expect(store.data.get(WINDOW_ID_STORAGE_KEY)).toBe("window-1");
  });

  it("filters malformed tab values", () => {
    const store = storage();
    store.data.set(BRANCH_TABS_STORAGE_KEY, JSON.stringify({ view: ["main", "", 3, "branch"] }));
    expect(loadBranchTabs(store, "view")).toEqual(["main", "branch"]);
  });

  it("stores default tabs as absence and removes the legacy window key", () => {
    const store = storage();
    writeBranchTabs(store, "window:view", "view", ["branch"]);
    expect(loadBranchTabs(store, "window:view")).toEqual(["branch"]);
    writeBranchTabs(store, "window:view", "view", ["main"]);
    expect(loadBranchTabs(store, "window:view")).toEqual([]);
    expect(loadBranchTabs(store, "view")).toEqual([]);
  });
});
