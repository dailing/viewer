import { describe, expect, it, vi } from "vitest";

import type { DockProvider } from "./definePlugin";
import { collectDockEntries, openProvider } from "./dockModel";

function provider(values: Partial<DockProvider> = {}): DockProvider {
  return {
    type: "tool",
    icon: "bi-puzzle",
    title: "Tool",
    instances: [],
    ...values,
  };
}

describe("Dock model", () => {
  it("does not create permanent entries without explicit instances", () => {
    expect(collectDockEntries([provider()])).toEqual([]);
  });

  it("docks explicit instances", () => {
    const files = provider({
      type: "files",
      instances: [{ id: "pinned", label: "Pinned file", icon: "bi-file-earmark" }],
    });
    const entries = collectDockEntries([files]);
    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({ uid: "files:pinned", icon: "bi-file-earmark" });
  });

  it("opens singleton main through the unified opener", async () => {
    const opener = { open: vi.fn() };
    await openProvider(provider({ type: "plugin-manager" }), opener);
    expect(opener.open).toHaveBeenCalledWith({ paneType: "plugin-manager", instanceId: "main" });
  });

  it("opens the instance id returned by create", async () => {
    const opener = { open: vi.fn() };
    const browser = provider({ type: "browser", create: () => Promise.resolve("pg_123") });
    await openProvider(browser, opener);
    expect(opener.open).toHaveBeenCalledWith({ paneType: "browser", instanceId: "pg_123" });
  });
});
