import { describe, expect, it, vi } from "vitest";

import type { DockProvider } from "./definePlugin";
import { collectDockEntries, collectDockSections, openProvider } from "./dockModel";

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

  it("groups Task entries under weak Chat headers without grouping other plugins", () => {
    const chat = provider({
      type: "chat",
      instances: [
        { id: "view-a", label: "Task A", group: { id: "chat-1", label: "Project One" } },
        { id: "view-b", label: "Task B", group: { id: "chat-1", label: "Project One" } },
        { id: "view-c", label: "Task C", group: { id: "chat-2", label: "Project Two" } },
      ],
    });
    const files = provider({ type: "files", instances: [{ id: "file", label: "Notes" }] });
    const sections = collectDockSections([chat, files]);
    expect(sections.map((section) => section.group?.label)).toEqual(["Project One", "Project Two", undefined]);
    expect(sections.map((section) => section.entries.map((entry) => entry.displayLabel))).toEqual([
      ["Task A", "Task B"],
      ["Task C"],
      ["Notes"],
    ]);
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

  it("uses the group-specific factory for a Task create action", async () => {
    const opener = { open: vi.fn() };
    const createForGroup = vi.fn(() => Promise.resolve("task-view"));
    const chat = provider({ type: "chat", createForGroup });
    await openProvider(chat, opener, "chat-1");
    expect(createForGroup).toHaveBeenCalledWith("chat-1");
    expect(opener.open).toHaveBeenCalledWith({ paneType: "chat", instanceId: "task-view" });
  });
});
