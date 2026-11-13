/**
 * Task Dock unread dots (unread / unread-error).
 *
 * Running state is deliberately not stored here: each Dock row reads the
 * authoritative `running` flag of its own Task. Completion and read state are
 * keyed by Task ID so one Task can never mark its Chat siblings unread.
 * Read state is browser-local; Tasks absent from the map are read.
 */
import { reactive } from "vue";

const STORAGE_KEY = "viewer.taskUnread.v1";

export type UnreadKind = "done" | "error";
export type TaskDockState = "error" | "unread" | undefined;

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

export const taskDockStatus = reactive({
  /** Task id → unread kind; absence means read. Persisted. */
  unread: loadUnread(),
});

function persistUnread(): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(taskDockStatus.unread));
  } catch {
    // Quota/private-mode failures are non-fatal: dots remain session-local.
  }
}

/** Record a finished Turn only when its own Task has no open pane. */
export function markTurnCompleted(taskID: string, stopReason: string, taskOpen: boolean): void {
  if (taskID === "" || taskOpen) return;
  taskDockStatus.unread[taskID] = isErrorStopReason(stopReason) ? "error" : "done";
  persistUnread();
}

/** Opening one Task marks only that Task read. */
export function markTaskRead(taskID: string): void {
  if (taskID in taskDockStatus.unread) {
    delete taskDockStatus.unread[taskID];
    persistUnread();
  }
}

/** Unread dot state for one Task: error wins over done. */
export function dockUnreadStateFor(taskID: string): TaskDockState {
  const kind = taskDockStatus.unread[taskID];
  if (kind === "error") return "error";
  if (kind === "done") return "unread";
  return undefined;
}
