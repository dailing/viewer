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
  it("does not create permanent entries for singleton tools", () => {
    const singleton = provider({ singleton: true });
    expect(collectDockEntries([singleton])).toEqual([]);
  });

  it("docks only explicit instances, including those of a singleton provider", () => {
    const files = provider({
      type: "files",
      singleton: true,
      instances: [{ id: "pinned", label: "Pinned file", icon: "bi-file-earmark" }],
    });
    const entries = collectDockEntries([files]);
    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({ uid: "files:pinned", icon: "bi-file-earmark" });
  });

  it("opens a singleton main pane from the plus menu", async () => {
    const opener = { openInstance: vi.fn() };
    await openProvider(provider({ type: "plugin-manager", singleton: true }), opener);
    expect(opener.openInstance).toHaveBeenCalledWith("plugin-manager", "main");
  });

  it("opens the instance id returned by create", async () => {
    const opener = { openInstance: vi.fn() };
    const browser = provider({ type: "browser", create: () => Promise.resolve("pg_123") });
    await openProvider(browser, opener);
    expect(opener.openInstance).toHaveBeenCalledWith("browser", "pg_123");
  });
});
