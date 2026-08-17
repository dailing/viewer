/**
 * Chat pane settings: browser-local toggles for the Super Workspace chat
 * timeline, persisted to localStorage (non-namespaced, like the layout and
 * markdown-style stores).
 *
 * `virtualSpace` (old-viewer parity, legacy `chat_virtual_space_enabled`):
 * when on, the thread renders one viewport of empty space after the final
 * message, and initial loads / newly sent queries scroll to the message-end
 * anchor instead of the absolute scroll-container end — the latest message
 * lands at the normal lower edge while the reader can manually move it
 * toward the middle or top of the pane.
 */
import { defineStore } from "pinia";

const STORAGE_KEY = "viewer.chatSettings.v1";

interface ChatSettingsState { virtualSpace: boolean }

function loadSettings(): ChatSettingsState {
  const raw = localStorage.getItem(STORAGE_KEY);
  if (raw) {
    try {
      const parsed = JSON.parse(raw) as Partial<ChatSettingsState>;
      if (parsed && typeof parsed === "object") {
        return { virtualSpace: parsed.virtualSpace !== false }; // default on
      }
    } catch { /* fall through to defaults */ }
  }
  return { virtualSpace: true };
}

export const useChatSettingsStore = defineStore("chatSettings", {
  state: (): ChatSettingsState => loadSettings(),
  actions: {
    setVirtualSpace(value: boolean): void {
      this.virtualSpace = value;
      this.persist();
    },
    toggleVirtualSpace(): void {
      this.setVirtualSpace(!this.virtualSpace);
    },
    persist(): void {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ virtualSpace: this.virtualSpace }));
    },
  },
});
