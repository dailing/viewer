import { describe, expect, it } from "vitest";
import { mergeMessageWindow, readingAnchor, withReadingAnchor } from "./historyWindow";

function message(id: string, createdAt: number) { return { id, created_at: createdAt }; }

describe("Task history window", () => {
  it("deduplicates, deletes and caps the DOM window at 300 messages", () => {
    const initial = Array.from({ length: 300 }, (_, index) => message(`m${index}`, index + 1));
    const result = mergeMessageWindow(initial, [message("m0", 1), message("m300", 301), message("m301", 302), { ...message("m2", 3), deleted: true }]);
    expect(result.messages).toHaveLength(300);
    expect(result.evictedOlder).toBe(true);
    expect(result.messages[0]?.id).toBe("m1");
    expect(result.messages.at(-1)?.id).toBe("m301");
    expect(result.messages.some((item) => item.id === "m2")).toBe(false);
  });

  it("keeps reading anchors isolated by view-local Task id", () => {
    const anchors = withReadingAnchor({}, "task-a", { createdAt: 10, messageId: "m10" });
    const updated = withReadingAnchor(anchors, "task-b", { createdAt: 20, messageId: "m20" });
    expect(readingAnchor(updated, "task-a")).toEqual({ createdAt: 10, messageId: "m10" });
    expect(readingAnchor(updated, "task-b")).toEqual({ createdAt: 20, messageId: "m20" });
    expect(readingAnchor(updated, "task-c")).toBeUndefined();
    expect(readingAnchor({ "task-a": { createdAt: -1, messageId: "bad" } }, "task-a")).toBeUndefined();
  });
});
