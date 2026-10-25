import { describe, expect, it } from "vitest";
import { beyondWindowEdge, collectDeltaPages, cursorOf, mergeMessages, trimWindow, type ChatWindow, type WindowMessage } from "./chatWindow";

function rows(count: number, start = 0): WindowMessage[] {
  return Array.from({ length: count }, (_, index) => ({ id: `m${start + index}`, created_at: start + index }));
}

function window(messages: WindowMessage[], detached = false): ChatWindow<WindowMessage> {
  return {
    messages,
    hasOlder: false,
    olderCursor: cursorOf(messages[0]),
    loadedLo: messages[0]?.created_at ?? 0,
    hasNewer: detached,
    loadedHi: detached ? cursorOf(messages[messages.length - 1]) : null,
  };
}

describe("collectDeltaPages", () => {
  it("reports an incomplete catch-up at the page cap instead of claiming the live edge", async () => {
    const all = rows(201);
    const result = await collectDeltaPages(cursorOf(all[0]), async (cursor) => {
      const start = all.findIndex((item) => item.id === cursor?.id);
      const page = all.slice(start, start + 50);
      return { messages: page, hasMore: start + 50 < all.length };
    });
    expect(result.pages).toHaveLength(3);
    expect(result.pages.flat()).toHaveLength(150);
    expect(result.incomplete).toBe(true);
  });

  it("completes when the third page reaches the edge", async () => {
    const all = rows(148);
    const result = await collectDeltaPages(cursorOf(all[0]), async (cursor) => {
      const start = all.findIndex((item) => item.id === cursor?.id);
      const page = all.slice(start, start + 50);
      return { messages: page, hasMore: start + 50 < all.length };
    });
    expect(result.incomplete).toBe(false);
    expect(result.pages.flat()).toHaveLength(150);
  });

  it("does not interpret an initial top page's older-rows flag as newer rows", async () => {
    const result = await collectDeltaPages(null, async () => ({ messages: rows(50), hasMore: true }));
    expect(result.pages).toHaveLength(1);
    expect(result.incomplete).toBe(false);
  });
});

describe("chat window transitions", () => {
  it("replaces streaming rows by id and restores cursor order", () => {
    const merged = mergeMessages(rows(2), [{ id: "m1", created_at: 1 }, { id: "m0", created_at: 0 }]);
    expect(merged.map((item) => item.id)).toEqual(["m0", "m1"]);
  });

  it("evicts the top of an attached over-cap window and keeps the live tail", () => {
    const result = trimWindow(window(rows(350)), 300, 50, "top");
    expect(result.messages).toHaveLength(300);
    expect(result.messages[0].id).toBe("m50");
    expect(result.messages[299].id).toBe("m349");
    expect(result.hasOlder).toBe(true);
    expect(result.loadedLo).toBe(50);
    expect(result.hasNewer).toBe(false);
  });

  it("evicts the bottom while reading upward in a detached window", () => {
    const result = trimWindow(window(rows(350), true), 300, 50, "bottom");
    expect(result.messages).toHaveLength(300);
    expect(result.messages[299].id).toBe("m299");
    expect(result.hasNewer).toBe(true);
    expect(result.loadedHi).toEqual({ ts: 299, id: "m299" });
  });

  it("recognizes only rows beyond the detached upper cursor", () => {
    const state = { hasNewer: true, loadedHi: { ts: 10, id: "b" } };
    expect(beyondWindowEdge(state, 10, "a")).toBe(false);
    expect(beyondWindowEdge(state, 10, "c")).toBe(true);
    expect(beyondWindowEdge(state, 11, "a")).toBe(true);
    expect(beyondWindowEdge({ ...state, hasNewer: false }, 11, "a")).toBe(false);
  });
});
