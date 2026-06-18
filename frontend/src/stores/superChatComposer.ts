import { defineStore } from "pinia";
import { namespacedStorageKey } from "../utils/userProfile";

const PIN_STORAGE_KEY = "viewer.superChatComposerPins.v1";
const DRAFT_STORAGE_KEY = "viewer.superChatComposerDrafts.v1";
const DRAFT_WRITE_DELAY_MS = 450;
let draftWriteTimer: number | null = null;
let pendingDrafts: Record<string, string> | null = null;

function readPins(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(namespacedStorageKey(PIN_STORAGE_KEY));
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
  localStorage.setItem(namespacedStorageKey(PIN_STORAGE_KEY), JSON.stringify(value));
}

function readDrafts(): Record<string, string> {
  try {
    const raw = localStorage.getItem(namespacedStorageKey(DRAFT_STORAGE_KEY));
    const parsed = raw ? JSON.parse(raw) : {};
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const drafts: Record<string, string> = {};
    for (const [key, value] of Object.entries(parsed)) {
      if (key && typeof value === "string") drafts[key] = value;
    }
    return drafts;
  } catch {
    return {};
  }
}

function writeDrafts(value: Record<string, string>) {
  localStorage.setItem(namespacedStorageKey(DRAFT_STORAGE_KEY), JSON.stringify(value));
}

function flushDrafts() {
  if (draftWriteTimer !== null) {
    window.clearTimeout(draftWriteTimer);
    draftWriteTimer = null;
  }
  if (!pendingDrafts) return;
  writeDrafts(pendingDrafts);
  pendingDrafts = null;
}

function scheduleDraftWrite(value: Record<string, string>) {
  pendingDrafts = { ...value };
  if (draftWriteTimer !== null) window.clearTimeout(draftWriteTimer);
  draftWriteTimer = window.setTimeout(() => flushDrafts(), DRAFT_WRITE_DELAY_MS);
}

if (typeof window !== "undefined") {
  window.addEventListener("pagehide", flushDrafts);
  window.addEventListener("beforeunload", flushDrafts);
}

export const useSuperChatComposerStore = defineStore("superChatComposer", {
  state: () => ({
    pinnedByChatId: readPins(),
    draftByChatId: readDrafts(),
  }),
  getters: {
    isPinned:
      (state) =>
      (chatId: string): boolean =>
        chatId ? state.pinnedByChatId[chatId] ?? true : true,
    draft:
      (state) =>
      (chatId: string): string =>
        chatId ? state.draftByChatId[chatId] ?? "" : "",
  },
  actions: {
    setPinned(chatId: string, pinned: boolean) {
      if (!chatId) return;
      this.pinnedByChatId = { ...this.pinnedByChatId, [chatId]: pinned };
      writePins(this.pinnedByChatId);
    },
    togglePinned(chatId: string): boolean {
      const pinned = !this.isPinned(chatId);
      this.setPinned(chatId, pinned);
      return pinned;
    },
    setDraft(chatId: string, draft: string) {
      if (!chatId) return;
      if (draft) {
        this.draftByChatId = { ...this.draftByChatId, [chatId]: draft };
      } else {
        const next = { ...this.draftByChatId };
        delete next[chatId];
        this.draftByChatId = next;
      }
      scheduleDraftWrite(this.draftByChatId);
    },
    clearDraft(chatId: string) {
      this.setDraft(chatId, "");
      flushDrafts();
    },
    flushDrafts,
  },
});
