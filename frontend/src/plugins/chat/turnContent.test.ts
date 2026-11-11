import { describe, expect, it } from "vitest";
import { visibleMessageText, visibleTurnItems, type ChatTimelineItem } from "./turnContent";
import type { ChatMessage, Decision } from "./types";

const message: ChatMessage = {
  id: "message-a", chat_id: "chat-a", task_id: "task-a", turn_id: "turn-a", role: "assistant",
  text: "Visible answer\n<viewer-decision>\nversion: 1\nkind: choice\n</viewer-decision>", created_at: 1,
  sender: { from: "role", role_id: "role-a" },
};
const decision = { source_message_id: message.id, marker_start: message.text.indexOf("<viewer-decision>"), marker_end: message.text.length } as Decision;

describe("shared visible Turn content", () => {
  it("uses the same valid-marker stripping as the Task timeline", () => {
    expect(visibleMessageText(message, [decision])).toBe("Visible answer\n");
  });
  it("keeps invalid marker source unchanged", () => {
    expect(visibleMessageText(message, [{ ...decision, status: "invalid" }])).toBe(message.text);
  });
  it("filters every activity kind from Decision context while preserving the full Task timeline", () => {
    const items: ChatTimelineItem[] = [
      { id: "message-a", at: 1, kind: "message", message },
      { id: "tool-a", at: 2, kind: "activity" },
      { id: "reasoning-a", at: 3, kind: "activity" },
    ];
    expect(visibleTurnItems(items, false).map((item) => item.id)).toEqual(["message-a"]);
    expect(visibleTurnItems(items, true)).toHaveLength(3);
  });
});
