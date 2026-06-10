import { defineStore } from "pinia";
import { namespacedStorageKey } from "../utils/userProfile";

const STORAGE_KEY = "viewer.superChatComposerPins.v1";

function readPins(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(namespacedStorageKey(STORAGE_KEY));
    const parsed = raw ? JSON.parse(raw) : {};
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const pins: Record<string, boolean> = {};
    for (const [key, value] of Object.entries(parsed)) {
      if (key && typeof value === "boolean") pins[key] = value;
    }
    return pins;
  } catch {
    return {};
  }
}

function writePins(value: Record<string, boolean>) {
  localStorage.setItem(namespacedStorageKey(STORAGE_KEY), JSON.stringify(value));
}

export const useSuperChatComposerStore = defineStore("superChatComposer", {
  state: () => ({
    pinnedByChatId: readPins(),
  }),
  getters: {
    isPinned:
      (state) =>
      (chatId: string): boolean =>
        Boolean(chatId && state.pinnedByChatId[chatId]),
  },
  actions: {
    setPinned(chatId: string, pinned: boolean) {
      if (!chatId) return;
      if (pinned) {
        this.pinnedByChatId = { ...this.pinnedByChatId, [chatId]: true };
      } else {
        const next = { ...this.pinnedByChatId };
        delete next[chatId];
        this.pinnedByChatId = next;
      }
      writePins(this.pinnedByChatId);
    },
    togglePinned(chatId: string): boolean {
      const pinned = !this.isPinned(chatId);
      this.setPinned(chatId, pinned);
      return pinned;
    },
  },
});
