import { describe, expect, it } from "vitest";
import { loadEntry, removeEntry, saveEntry, type ChatCacheEntry } from "./chatCache";

function entry(): ChatCacheEntry {
  return {
    chat: null,
    messages: [],
    blocks: [],
    roles: [],
    workspace: null,
    hasOlder: true,
    olderCursor: { ts: 10, id: "old" },
    loadedLo: 10,
    blockHigh: 0,
    hasNewer: true,
    loadedHi: { ts: 20, id: "new" },
    turnTargets: {},
  };
}

describe("chatCache detached state", () => {
  it("round-trips the upper cursor so it cannot be mistaken for an attached window", () => {
    const key = "chat-cache-test\x00main";
    saveEntry(key, entry());
    const restored = loadEntry(key);
    expect(restored?.hasNewer).toBe(true);
    expect(restored?.loadedHi).toEqual({ ts: 20, id: "new" });
    removeEntry(key);
  });
});
