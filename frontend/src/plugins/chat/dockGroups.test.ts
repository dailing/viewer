import { describe, expect, it } from "vitest";

import { chatDockGroups } from "./dockGroups";

function summaries(chats: Parameters<typeof chatDockGroups>[0], tasks: Parameters<typeof chatDockGroups>[1]) {
  return chatDockGroups(chats, tasks).map((group) => ({ id: group.id, label: group.label, meta: group.meta?.() }));
}

function counts(running: number, idle: number) {
  return [
    { text: running, tone: "success", title: `${running} running` },
    { text: idle, title: `${idle} idle` },
  ];
}

describe("Chat Dock groups", () => {
  it("keeps pinned Chats without open Tasks and includes unpinned Chats only with Tasks", () => {
    const chats = [
      { id: "pinned-empty", name: "Pinned Empty", pinned: true },
      { id: "unpinned-empty", name: "Unpinned Empty", pinned: false },
      { id: "unpinned-open", name: "Unpinned Open", pinned: false },
      { id: "pinned-open", name: "Pinned Open", pinned: true },
    ];
    const tasks = [
      { chat_id: "unpinned-open", chat_name: "Unpinned Open", running: false },
      { chat_id: "pinned-open", chat_name: "Pinned Open", running: true },
    ];
    expect(summaries(chats, tasks)).toEqual([
      { id: "pinned-empty", label: "Pinned Empty", meta: counts(0, 0) },
      { id: "unpinned-open", label: "Unpinned Open", meta: counts(0, 1) },
      { id: "pinned-open", label: "Pinned Open", meta: counts(1, 0) },
    ]);
  });

  it("counts running and idle Tasks independently within each Chat", () => {
    const tasks = [
      { chat_id: "chat", chat_name: "Project", running: true },
      { chat_id: "chat", chat_name: "Project", running: true },
      { chat_id: "chat", chat_name: "Project", running: false },
      { chat_id: "chat", chat_name: "Project" },
    ];
    expect(summaries([], tasks)).toEqual([
      { id: "chat", label: "Project", meta: counts(2, 2) },
    ]);
  });

  it("removes an empty Chat group when its pin is cleared", () => {
    const chat = { id: "chat", name: "Project", pinned: true };
    expect(summaries([chat], [])).toEqual([{ id: "chat", label: "Project", meta: counts(0, 0) }]);
    expect(summaries([{ ...chat, pinned: false }], [])).toEqual([]);
  });

  it("uses Task metadata if its Chat is absent from the Chat list", () => {
    expect(summaries([], [{ chat_id: "chat", chat_name: "Recovered Chat", running: false }])).toEqual([
      { id: "chat", label: "Recovered Chat", meta: counts(0, 1) },
    ]);
  });
});
