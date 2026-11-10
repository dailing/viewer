import { describe, expect, it } from "vitest";
import type { ChatBlock } from "./types";
import { activityDisplayable, activityGroupSummary, groupActivityItems, type ActivityTimelineItem } from "./toolPresentation";

function block(id: string, kind = "tool_call", occurredAt = 1): ChatBlock {
  return { id, chat_id: "chat", task_id: "task", turn_id: "turn", kind, text: `text-${id}`, payload: "", occurred_at: occurredAt };
}

function activity(id: string, at: number): ActivityTimelineItem {
  return { id, at, kind: "activity", block: block(id, "thinking", at) };
}

describe("chat activity timeline", () => {
  it("keeps body text and protocol noise out of activity rows", () => {
    expect(activityDisplayable(block("body", "agent_text"))).toBe(false);
    expect(activityDisplayable(block("noise", "other"))).toBe(false);
    expect(activityDisplayable(block("unknown", "future_kind"))).toBe(false);
    for (const kind of ["tool_call", "tool_result", "file_change", "command", "thinking", "error"]) {
      expect(activityDisplayable(block(kind, kind))).toBe(true);
    }
  });

  it("collapses only consecutive runs of at least three activities", () => {
    const message: ActivityTimelineItem = { id: "message", at: 4, kind: "message" };
    const grouped = groupActivityItems([
      activity("a", 1), activity("b", 2), activity("c", 3), message,
      activity("d", 5), activity("e", 6),
    ]);
    expect(grouped.map((item) => item.kind)).toEqual(["activity-group", "message", "activity", "activity"]);
    const group = grouped[0];
    expect(group.kind).toBe("activity-group");
    if (group.kind !== "activity-group") return;
    expect(group.id).toBe("tool-group:a");
    expect(group.at).toBe(3);
    expect(group.items.map((item) => item.id)).toEqual(["a", "b", "c"]);
    expect(activityGroupSummary(group)).toBe("text-c");
    expect(grouped[2]).toMatchObject({ id: "d" });
    expect(grouped[3]).toMatchObject({ id: "e" });
  });
});
