import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

import { EXPANDED_GROUPS_KEY, useDockSettingsStore } from "./dockSettings";

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

describe("Dock group persistence", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", storageMock());
    setActivePinia(createPinia());
  });

  afterEach(() => vi.unstubAllGlobals());

  it("round-trips expanded Chat groups across store recreation", () => {
    const settings = useDockSettingsStore();
    settings.setGroupExpanded("chat-1", true);
    settings.setGroupExpanded("chat-2", true);
    settings.setGroupExpanded("chat-1", false);
    expect(JSON.parse(localStorage.getItem(EXPANDED_GROUPS_KEY) ?? "[]")).toEqual(["chat-2"]);

    setActivePinia(createPinia());
    const restored = useDockSettingsStore();
    expect(restored.expandedGroups).toEqual(["chat-2"]);
    restored.setGroupExpanded("chat-2", false);
    expect(JSON.parse(localStorage.getItem(EXPANDED_GROUPS_KEY) ?? "[]")).toEqual([]);
  });

  it("filters malformed persisted groups and starts collapsed by default", () => {
    localStorage.setItem(EXPANDED_GROUPS_KEY, JSON.stringify(["chat-1", "chat-1", "", 7, null]));
    setActivePinia(createPinia());
    expect(useDockSettingsStore().expandedGroups).toEqual(["chat-1"]);

    localStorage.setItem(EXPANDED_GROUPS_KEY, "not-json");
    setActivePinia(createPinia());
    expect(useDockSettingsStore().expandedGroups).toEqual([]);
  });
});
