import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

import { hostedInstanceIds, openShellInstance } from "./instances";
import {
  componentRegistry,
  instanceRegistry,
  registerComponent,
  registerInstanceAdapter,
  unregisterInstanceAdapter,
} from "./registries";
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

describe("Shell instance opening", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", storageMock());
    setActivePinia(createPinia());
    componentRegistry.clear();
    instanceRegistry.clear();
  });

  it("opens a singleton main instance without params", async () => {
    registerComponent("settings", {});
    const opened = await openShellInstance({ paneType: "settings" });
    expect(opened).toEqual({ paneId: "p1", instanceId: "main" });
    expect(useLayoutStore().panes[0]?.content).toEqual({ paneType: "settings", instanceId: "main" });
  });

  it("creates a multi-instance view from structured params", async () => {
    const open = vi.fn((request: { params?: unknown }) => {
      expect(request.params).toEqual({ chatId: "c1", initialTabs: ["b1"] });
      return "view-1";
    });
    registerComponent("chat", {});
    registerInstanceAdapter({ type: "chat", cardinality: "multi", open });
    const opened = await openShellInstance({ paneType: "chat", params: { chatId: "c1", initialTabs: ["b1"] } });
    expect(opened.instanceId).toBe("view-1");
    expect(open).toHaveBeenCalledOnce();
  });

  it("updates an existing instance through the same adapter", async () => {
    registerComponent("file-viewer", {});
    registerInstanceAdapter({
      type: "file-viewer",
      cardinality: "multi",
      open: (request) => {
        expect(request).toMatchObject({ instanceId: "viewer-1", params: { path: "/tmp/a.md" } });
        return request.instanceId ?? "";
      },
    });
    const opened = await openShellInstance({
      paneType: "file-viewer",
      instanceId: "viewer-1",
      params: { path: "/tmp/a.md" },
    });
    expect(opened.instanceId).toBe("viewer-1");
  });

  it("forces singleton adapters to resolve main", async () => {
    registerComponent("tool", {});
    registerInstanceAdapter({ type: "tool", cardinality: "singleton", open: () => "main" });
    await expect(openShellInstance({ paneType: "tool", instanceId: "other" })).rejects.toThrow("singleton");
  });

  it("rejects unknown types before changing layout", async () => {
    const layout = useLayoutStore();
    await expect(openShellInstance({ paneType: "missing", params: {} })).rejects.toThrow("unknown pane type");
    expect(layout.panes[0]?.content).toBeNull();
  });

  it("keeps floating instances in the hosted-id set", async () => {
    registerComponent("file-manager", {});
    registerInstanceAdapter({ type: "file-manager", cardinality: "multi", open: () => "manager-1" });
    const opened = await openShellInstance({ paneType: "file-manager", params: { dir: "/tmp" } });
    useLayoutStore().floatPane(opened.paneId);
    expect(hostedInstanceIds("file-manager")).toEqual(new Set(["manager-1"]));
  });

  it("removes adapters on hot unload", async () => {
    registerComponent("external", {});
    registerInstanceAdapter({ type: "external", cardinality: "multi", open: () => "x" });
    unregisterInstanceAdapter("external");
    await expect(openShellInstance({ paneType: "external", params: { path: "/x" } })).rejects.toThrow("adapter");
  });
});
