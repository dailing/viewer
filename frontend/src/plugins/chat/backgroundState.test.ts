import { describe, expect, it } from "vitest";
import { backgroundSnapshot, reduceBackgroundEvent } from "./backgroundState";
import type { BackgroundTurn } from "./types";

function background(overrides: Partial<BackgroundTurn> = {}): BackgroundTurn {
  return {
    chat_id: "chat-a", task_id: "task-a", turn_id: "turn-a", role_id: "role-a", role_name: "Utility",
    session_id: "session-a", active: true, since: 123,
    processes: [{ session_id: "process-a", command: "sleep 30", pid: 42 }], ...overrides,
  };
}

describe("Task background process state", () => {
  it("restores only active source Turns from history", () => {
    const snapshot = backgroundSnapshot([background(), background({ turn_id: "turn-b", active: false })]);
    expect([...snapshot.keys()]).toEqual(["turn-a"]);
    expect(snapshot.get("turn-a")?.processes[0].command).toBe("sleep 30");
  });

  it("updates counts in place and releases only the matching Turn", () => {
    const initial = backgroundSnapshot([background(), background({ turn_id: "turn-b" })]);
    const updated = reduceBackgroundEvent(initial, background({ processes: [] }));
    expect(updated.get("turn-a")?.processes).toHaveLength(0);
    expect(updated.has("turn-b")).toBe(true);

    const released = reduceBackgroundEvent(updated, background({ active: false, processes: [] }));
    expect(released.has("turn-a")).toBe(false);
    expect(released.has("turn-b")).toBe(true);
  });

  it("clears a Task snapshot for a legacy inactive frame without Turn identity", () => {
    const initial = backgroundSnapshot([background()]);
    expect(reduceBackgroundEvent(initial, background({ turn_id: "", active: false })).size).toBe(0);
  });
});
