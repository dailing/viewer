import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  chatIdForView,
  clearChatViewsForTest,
  createAdditionalChatView,
  ensureDefaultChatView,
  resolveChatView,
  restoreChatView,
} from "./viewInstances";

describe("Chat view instances", () => {
  beforeEach(() => {
    const values = new Map<string, string>();
    vi.stubGlobal("localStorage", {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value); },
      removeItem: (key: string) => { values.delete(key); },
      clear: () => { values.clear(); },
    });
    clearChatViewsForTest();
  });

  it("reuses one default view for ordinary Chat opens", () => {
    const first = ensureDefaultChatView("chat-1");
    expect(ensureDefaultChatView("chat-1")).toBe(first);
    expect(ensureDefaultChatView("chat-2")).not.toBe(first);
    expect(chatIdForView(first)).toBe("chat-1");
  });

  it("creates independent views with the same chatId and different initial branches", () => {
    const first = createAdditionalChatView({ chatId: "chat-1", initialTabs: ["b1"] });
    const second = createAdditionalChatView({ chatId: "chat-1", initialTabs: ["b2"] });
    expect(first).not.toBe(second);
    expect(first).not.toContain("chat-1");
    expect(resolveChatView(first)).toMatchObject({ chatId: "chat-1", initialTabs: ["b1"], defaultForChat: false });
    expect(resolveChatView(second)).toMatchObject({ chatId: "chat-1", initialTabs: ["b2"] });
  });

  it("restores known views and adopts legacy plain chat ids", () => {
    const known = createAdditionalChatView({ chatId: "chat-1" });
    expect(restoreChatView(known)).toBe(known);
    const legacy = restoreChatView("chat-legacy");
    expect(legacy).not.toBe("chat-legacy");
    expect(chatIdForView(legacy)).toBe("chat-legacy");
  });

  it("persists records for fixed input restoration", () => {
    const id = createAdditionalChatView({ chatId: "chat-1" });
    const stored = JSON.parse(localStorage.getItem("viewer.chatViews.v1") ?? "{}") as Record<string, unknown>;
    expect(stored[id]).toMatchObject({ chatId: "chat-1" });
  });
});
