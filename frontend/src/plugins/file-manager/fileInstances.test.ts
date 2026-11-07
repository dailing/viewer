import { afterEach, describe, expect, it, vi } from "vitest";
import type { PluginCtx } from "../../shell/ctx";
import { managerAdapter } from "./index";
import { createInstance as createManager, getInstance as getManager, instances as managers, pruneUnpinned as pruneManagers, setPinned as pinManager } from "./instanceStore";
import { openInPairedViewer } from "./viewerTarget";
import { viewerAdapter } from "../file-viewer/index";
import { getInstance as getViewer, instances as viewers, pruneUnpinned as pruneViewers, setPinned as pinViewer, updateState as updateViewer } from "../file-viewer/instanceStore";

vi.mock("../../shell/bus", () => ({
  bus: { request: vi.fn(async () => ({})), onStateChange: vi.fn() },
}));

afterEach(() => {
  managers.splice(0);
  viewers.splice(0);
  vi.restoreAllMocks();
});

describe("independent file manager and viewer instances", () => {
  it("creates and restores structured params without sharing state", async () => {
    const managerId = await managerAdapter().open({ paneType: "file-manager", params: { dir: "/tmp/a" } });
    const viewerId = await viewerAdapter().open({ paneType: "file-viewer", params: { path: "/tmp/a/readme.md" } });
    expect(managerId).not.toBe(viewerId);
    expect(getManager(managerId)?.state).toEqual({ dir: "/tmp/a", viewerId: null, showHidden: false });
    expect(getViewer(viewerId)?.state.path).toBe("/tmp/a/readme.md");
    expect(getViewer(viewerId)?.state).not.toHaveProperty("dir");
    await viewerAdapter().open({ paneType: "file-viewer", instanceId: viewerId, params: { path: "/tmp/a/other.pdf" } });
    expect(getViewer(viewerId)?.state.path).toBe("/tmp/a/other.pdf");
    expect(getManager(managerId)?.state.dir).toBe("/tmp/a");
  });

  it("pins and prunes the two registries independently", async () => {
    const manager = createManager({ dir: "/a" });
    const viewerId = await viewerAdapter().open({ paneType: "file-viewer", params: { path: "/a/x.md" } });
    pinManager(manager.id, true);
    pinViewer(viewerId, true);
    pruneManagers(new Set());
    pruneViewers(new Set());
    expect(getManager(manager.id)).toBeDefined();
    expect(getViewer(viewerId)).toBeDefined();
    pinManager(manager.id, false);
    pruneManagers(new Set());
    pruneViewers(new Set());
    expect(getManager(manager.id)).toBeUndefined();
    expect(getViewer(viewerId)).toBeDefined();
    updateViewer(viewerId, { mode: "source" });
    expect(getViewer(viewerId)?.state.mode).toBe("source");
  });

  it("reuses one paired viewer and focuses it through Shell", async () => {
    const manager = createManager({ dir: "/a" });
    const open = vi.fn(async (request: { instanceId?: string }) => ({ paneId: "p1", instanceId: request.instanceId ?? "viewer-1" }));
    const ctx = { shell: { open } } as unknown as PluginCtx;
    await openInPairedViewer(ctx, manager.id, "/a/one.md");
    await openInPairedViewer(ctx, manager.id, "/a/two.md");
    expect(open).toHaveBeenCalledTimes(2);
    expect(open.mock.calls[0][0]).toMatchObject({ paneType: "file-viewer", params: { path: "/a/one.md" }, newPane: true });
    expect(open.mock.calls[1][0]).toMatchObject({ instanceId: "viewer-1", params: { path: "/a/two.md" } });
    expect(getManager(manager.id)?.state.viewerId).toBe("viewer-1");
  });
});
