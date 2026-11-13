import { describe, expect, it } from "vitest";
import type { ChatMessage, Decision } from "../chat/types";
import { decisionContextItems, decisionStatusIcon, decisionStatusLabel } from "./decisionPresentation";

function message(id: string, role: ChatMessage["role"], text: string, createdAt: number): ChatMessage {
  return {
    id, chat_id: "chat-a", task_id: "task-a", turn_id: "turn-a", role, text, created_at: createdAt,
    sender: role === "user" ? { from: "user" } : { from: "role", role_id: "role-a", role_name: "Agent" },
  };
}

const source = message("assistant-b", "assistant", "Answer\n<viewer-decision>\nversion: 1\nkind: choice\n</viewer-decision>", 3);
const decision = {
  source_message_id: source.id,
  marker_start: new TextEncoder().encode("Answer\n").length,
  marker_end: new TextEncoder().encode(source.text).length,
} as Decision;

describe("Decision Center presentation", () => {
  it("merges the Turn's consecutive assistant fragments after stripping the source marker", () => {
    const items = decisionContextItems([
      message("user-a", "user", "Do the work", 1),
      message("assistant-a", "assistant", "First part.", 2),
      source,
    ], decision);
    expect(items).toHaveLength(2);
    expect(items[0].message?.role).toBe("user");
    expect(items[1].message?.role).toBe("assistant");
    expect(items[1].message?.text).toBe("First part.\n\nAnswer\n");
    expect(items[1].message?.text).not.toContain("<viewer-decision>");
    expect(items[1].at).toBe(3);
  });

  it("does not merge assistant messages across a user message", () => {
    const items = decisionContextItems([
      message("assistant-a", "assistant", "Before", 1),
      message("user-a", "user", "Question", 2),
      message("assistant-b", "assistant", "After", 3),
    ], { ...decision, source_message_id: "not-present" });
    expect(items.map((item) => item.message?.text)).toEqual(["Before", "Question", "After"]);
  });

  it("uses compact history labels and icons", () => {
    expect(decisionStatusLabel({ status: "resolved" } as Decision)).toBe("finished");
    expect(decisionStatusLabel({ status: "cancelled" } as Decision)).toBe("cancelled");
    expect(decisionStatusLabel({ status: "invalid" } as Decision)).toBe("error");
    expect(decisionStatusIcon({ status: "invalid" } as Decision)).toBe("bi-exclamation-triangle");
  });
});
