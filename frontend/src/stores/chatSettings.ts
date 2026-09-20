/**
 * Chat pane settings: browser-local toggles for the Super Workspace chat
 * timeline, persisted to localStorage (non-namespaced, like the layout
 * store).
 *
 * `virtualSpace` (old-viewer parity, legacy `chat_virtual_space_enabled`):
 * when on, the thread renders one viewport of empty space after the final
 * message, and initial loads / newly sent queries scroll to the message-end
 * anchor instead of the absolute scroll-container end — the latest message
 * lands at the normal lower edge while the reader can manually move it
 * toward the middle or top of the pane.
 *
 * `turnNotifications`: when on, a finished turn posts a browser system
 * notification while the viewer tab is hidden (see plugins/chat/index.ts).
 * Enabling the toggle requests the Notification permission (a user gesture
 * is required); the effective gate at fire time is the live permission.
 */
import { defineStore } from "pinia";

const STORAGE_KEY = "viewer.chatSettings.v1";

interface ChatSettingsState { virtualSpace: boolean; turnNotifications: boolean }

function loadSettings(): ChatSettingsState {
  const raw = localStorage.getItem(STORAGE_KEY);
  if (raw) {
    try {
      const parsed = JSON.parse(raw) as Partial<ChatSettingsState>;
      if (parsed && typeof parsed === "object") {
        return {
          virtualSpace: parsed.virtualSpace !== false, // default on
          turnNotifications: parsed.turnNotifications === true, // default off
        };
      }
    } catch { /* fall through to defaults */ }
  }
  return { virtualSpace: true, turnNotifications: false };
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
    setTurnNotifications(value: boolean): void {
      this.turnNotifications = value;
      this.persist();
    },
    persist(): void {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ virtualSpace: this.virtualSpace, turnNotifications: this.turnNotifications }));
    },
  },
});
