import { describe, expect, it, vi } from "vitest";

import type { DockProvider } from "./definePlugin";
import { collectDockEntries, collectDockSections, openDockEntry, openProvider } from "./dockModel";

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

  it("keeps declared empty groups and merges their Task entries without duplication", () => {
    const chat = provider({
      type: "chat",
      groups: [
        { id: "chat-empty", label: "Pinned Empty" },
        { id: "chat-open", label: "Pinned Open" },
      ],
      instances: [{ id: "view-a", label: "Task A", group: { id: "chat-open", label: "Pinned Open" } }],
    });
    const sections = collectDockSections([chat]);
    expect(sections.map((section) => ({ label: section.group?.label, count: section.entries.length }))).toEqual([
      { label: "Pinned Empty", count: 0 },
      { label: "Pinned Open", count: 1 },
    ]);
  });

  it("docks plugin-declared singleton items with trailing metadata and actions", () => {
    const run = vi.fn();
    const tool = provider({
      instances: [{
        id: "main",
        label: "Decisions",
        icon: "bi-inbox",
        meta: () => 3,
        actions: [{ id: "refresh", title: "Refresh", icon: "bi-arrow-clockwise", run }],
      }],
    });
    const [entry] = collectDockEntries([tool]);
    expect(entry).toMatchObject({ uid: "tool:main", icon: "bi-inbox", label: "Decisions" });
    expect(entry.instance?.meta?.()).toBe(3);
    void entry.instance?.actions?.[0].run();
    expect(run).toHaveBeenCalledOnce();
  });

  it("uses a plugin-owned instance opener instead of the default shell route", async () => {
    const open = vi.fn();
    const opener = { open: vi.fn() };
    const [entry] = collectDockEntries([provider({ instances: [{ id: "main", label: "Tool", open }] })]);
    await openDockEntry(entry, opener);
    expect(open).toHaveBeenCalledOnce();
    expect(opener.open).not.toHaveBeenCalled();
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
