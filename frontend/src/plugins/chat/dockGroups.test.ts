import { describe, expect, it } from "vitest";

import { chatDockGroups } from "./dockGroups";

describe("Chat Dock groups", () => {
  it("keeps pinned Chats without open Tasks and includes unpinned Chats only with Tasks", () => {
    const chats = [
      { id: "pinned-empty", name: "Pinned Empty", pinned: true },
      { id: "unpinned-empty", name: "Unpinned Empty", pinned: false },
      { id: "unpinned-open", name: "Unpinned Open", pinned: false },
      { id: "pinned-open", name: "Pinned Open", pinned: true },
    ];
    const tasks = [
      { chat_id: "unpinned-open", chat_name: "Unpinned Open" },
      { chat_id: "pinned-open", chat_name: "Pinned Open" },
    ];
    expect(chatDockGroups(chats, tasks)).toEqual([
      { id: "pinned-empty", label: "Pinned Empty" },
      { id: "unpinned-open", label: "Unpinned Open" },
      { id: "pinned-open", label: "Pinned Open" },
    ]);
  });

  it("removes an empty Chat group when its pin is cleared", () => {
    const chat = { id: "chat", name: "Project", pinned: true };
    expect(chatDockGroups([chat], [])).toEqual([{ id: "chat", label: "Project" }]);
    expect(chatDockGroups([{ ...chat, pinned: false }], [])).toEqual([]);
  });

  it("uses Task metadata if its Chat is absent from the Chat list", () => {
    expect(chatDockGroups([], [{ chat_id: "chat", chat_name: "Recovered Chat" }])).toEqual([
      { id: "chat", label: "Recovered Chat" },
    ]);
  });
});
