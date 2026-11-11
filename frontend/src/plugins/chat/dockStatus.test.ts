import { beforeEach, describe, expect, it, vi } from "vitest";
import { dockUnreadStateFor, markTaskRead, markTurnCompleted, taskDockStatus } from "./dockStatus";

const stored = new Map<string, string>();
vi.stubGlobal("localStorage", {
  clear: () => stored.clear(),
  getItem: (key: string) => stored.get(key) ?? null,
  setItem: (key: string, value: string) => stored.set(key, value),
});

beforeEach(() => {
  localStorage.clear();
  for (const id of Object.keys(taskDockStatus.unread)) delete taskDockStatus.unread[id];
});

describe("Task Dock unread state", () => {
  it("never leaks completion or failure to sibling Tasks in the same Chat", () => {
    markTurnCompleted("task-a", "end_turn", false);
    expect(dockUnreadStateFor("task-a")).toBe("unread");
    expect(dockUnreadStateFor("task-b")).toBeUndefined();

    markTurnCompleted("task-b", "provider failed", false);
    expect(dockUnreadStateFor("task-a")).toBe("unread");
    expect(dockUnreadStateFor("task-b")).toBe("error");
    expect(localStorage.getItem("viewer.taskUnread.v1")).toBe(JSON.stringify({ "task-a": "done", "task-b": "error" }));
  });

  it("does not mark an open Task and clears only the Task being read", () => {
    markTurnCompleted("task-open", "end_turn", true);
    expect(dockUnreadStateFor("task-open")).toBeUndefined();

    markTurnCompleted("task-a", "end_turn", false);
    markTurnCompleted("task-b", "end_turn", false);
    markTaskRead("task-a");
    expect(dockUnreadStateFor("task-a")).toBeUndefined();
    expect(dockUnreadStateFor("task-b")).toBe("unread");
  });
});
