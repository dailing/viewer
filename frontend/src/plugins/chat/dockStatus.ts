/**
 * Chat Dock status dots (running / unread / unread-error).
 *
 * Semantics, driven by the backend `chat:_:turn` lifecycle feed plus the
 * `running_chat_ids` snapshot in chats:list:
 *
 * - "running": the chat has at least one in-flight turn (green dot). Live
 *   only in memory; rebuilt from chats:list on refresh, nudged by turn
 *   started/completed frames. A count per chat (not a boolean) because two
 *   dispatches on disjoint roles can overlap in one chat.
 * - "unread": a turn completed while the chat had no open pane (amber dot).
 * - "error": same, but the turn's stop_reason was a failure (red dot).
 *
 * Read state is browser-local (localStorage, non-namespaced like the other
 * shell stores): chats absent from the map are read, so a fresh browser
 * starts all-read. Opening a chat pane clears its entry; the dot disappears.
 */
import { reactive } from "vue";

const STORAGE_KEY = "viewer.chatUnread.v1";

export type UnreadKind = "done" | "error";

export type ChatDockState = "running" | "error" | "unread" | undefined;

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
  /** chat id → number of in-flight turns. */
  running: {} as Record<string, number>,
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

/** Adopt the backend snapshot (chats:list running_chat_ids). */
export function setRunningChats(ids: string[]): void {
  const next: Record<string, number> = {};
  for (const id of ids) {
    if (id !== "") next[id] = 1;
  }
  chatDockStatus.running = next;
}

export function markTurnStarted(chatID: string): void {
  if (chatID === "") return;
  chatDockStatus.running[chatID] = (chatDockStatus.running[chatID] ?? 0) + 1;
}

/**
 * Record a finished turn. When the chat has no open pane the completion is
 * unread: amber for a normal end, red when stop_reason reports a failure.
 */
export function markTurnCompleted(chatID: string, stopReason: string, chatOpen: boolean): void {
  if (chatID === "") return;
  const remaining = (chatDockStatus.running[chatID] ?? 0) - 1;
  if (remaining > 0) {
    chatDockStatus.running[chatID] = remaining;
  } else {
    delete chatDockStatus.running[chatID];
  }
  if (!chatOpen) {
    chatDockStatus.unread[chatID] = isErrorStopReason(stopReason) ? "error" : "done";
    persistUnread();
  }
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
  delete chatDockStatus.running[chatID];
  markChatRead(chatID);
}

/** Dock dot state for one chat: running wins over unread, error over done. */
export function dockStateFor(chatID: string): ChatDockState {
  if ((chatDockStatus.running[chatID] ?? 0) > 0) return "running";
  const kind = chatDockStatus.unread[chatID];
  if (kind === "error") return "error";
  if (kind === "done") return "unread";
  return undefined;
}
