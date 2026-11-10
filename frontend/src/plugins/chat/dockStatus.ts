/**
 * Chat Dock unread dots (unread / unread-error).
 *
 * Running state is deliberately not stored here: each Dock row reads the
 * authoritative `running` flag of its own Task. Chat-level running snapshots
 * must never leak onto sibling Tasks. Turn completion only records unread
 * state when the Chat has no open pane.
 *
 * Read state is browser-local (localStorage, non-namespaced like the other
 * shell stores): chats absent from the map are read, so a fresh browser
 * starts all-read. Opening a chat pane clears its entry; the dot disappears.
 */
import { reactive } from "vue";

const STORAGE_KEY = "viewer.chatUnread.v1";

export type UnreadKind = "done" | "error";
export type ChatDockState = "error" | "unread" | undefined;

/** Stop reasons that count as a failed turn for the red dot. */
export function isErrorStopReason(reason: string): boolean {
  return reason !== "" && reason !== "end_turn" && reason !== "cancelled";
}

function loadUnread(): Record<string, UnreadKind> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return {};
    const parsed = JSON.parse(raw) as unknown;
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return {};
    const result: Record<string, UnreadKind> = {};
    for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (value === "done" || value === "error") result[key] = value;
    }
    return result;
  } catch {
    return {};
  }
}

export const chatDockStatus = reactive({
  /** chat id → unread kind; absence means read. Persisted. */
  unread: loadUnread(),
});

function persistUnread(): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(chatDockStatus.unread));
  } catch {
    // Quota/private-mode failures are non-fatal: the dots just become
    // session-local for this browser.
  }
}

/**
 * Record a finished turn. When the chat has no open pane the completion is
 * unread: amber for a normal end, red when stop_reason reports a failure.
 */
export function markTurnCompleted(chatID: string, stopReason: string, chatOpen: boolean): void {
  if (chatID === "" || chatOpen) return;
  chatDockStatus.unread[chatID] = isErrorStopReason(stopReason) ? "error" : "done";
  persistUnread();
}

/** Opening a chat marks it read; the dot clears. */
export function markChatRead(chatID: string): void {
  if (chatID in chatDockStatus.unread) {
    delete chatDockStatus.unread[chatID];
    persistUnread();
  }
}

/** Drop all state for a removed chat. */
export function forgetChat(chatID: string): void {
  markChatRead(chatID);
}

/** Unread dot state for one chat: error wins over done. */
export function dockUnreadStateFor(chatID: string): ChatDockState {
  const kind = chatDockStatus.unread[chatID];
  if (kind === "error") return "error";
  if (kind === "done") return "unread";
  return undefined;
}
