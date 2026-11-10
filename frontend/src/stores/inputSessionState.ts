export type InputSessionPhase = "idle" | "sending" | "error";
export type InputSessionMode = "dispatch" | "queued-update";

/** The durable meaning of the text, not just the text itself.  A pane may
 * close or change its visible branch while a send is awaiting voice work. */
export interface InputSession {
  id: string;
  pluginId: string;
  paneType: string;
  instanceId: string;
  /** Opaque plugin target params, e.g. {chatId} for a Chat view. */
  params?: unknown;
  label: string;
  text: string;
  taskId: string;
  mode: InputSessionMode;
  queuedDispatchId: string;
  draftBeforeQueueEdit?: string;
  revision: number;
  selectedRoleIds: string[];
  forceNewSession: boolean;
  /** Raw state for the currently bound Task, mirrored in rawByTask. */
  raw: boolean;
  rawByTask: Record<string, boolean>;
  pinned: boolean;
  phase: InputSessionPhase;
  error: string;
  updatedAt: number;
}

export interface InputSessionIntent {
  taskId: string;
  mode: InputSessionMode;
  queuedDispatchId: string;
  draftBeforeQueueEdit?: string;
  selectedRoleIds: string[];
  forceNewSession: boolean;
  raw: boolean;
}

function bump(session: InputSession): void {
  session.revision = (session.revision ?? 0) + 1;
  session.updatedAt = Date.now();
}

export function inputSessionIntent(session: InputSession): InputSessionIntent {
  return {
    taskId: session.taskId ?? "",
    mode: session.mode ?? "dispatch",
    queuedDispatchId: session.queuedDispatchId ?? "",
    draftBeforeQueueEdit: session.draftBeforeQueueEdit,
    selectedRoleIds: [...session.selectedRoleIds],
    forceNewSession: session.forceNewSession,
    raw: session.rawByTask?.[session.taskId ?? ""] === true || session.raw === true,
  };
}

export function snapshotInputSession(
  session: InputSession,
  text = session.text,
  intent: InputSessionIntent = inputSessionIntent(session),
): InputSession {
  return {
    ...session,
    ...intent,
    selectedRoleIds: [...intent.selectedRoleIds],
    text,
  };
}

export function setInputSessionText(session: InputSession, text: string): void {
  if (session.text === text) return;
  session.text = text;
  bump(session);
}

export function setInputSessionTask(session: InputSession, taskId: string): void {
  if ((session.taskId ?? "") === taskId) return;
  // A queued edit belongs to its original entry even if the visible tabs
  // change. Restore the ordinary draft before re-binding the input.
  if (session.mode === "queued-update") cancelQueuedInputEdit(session);
  session.taskId = taskId;
  session.raw = session.rawByTask?.[taskId] === true;
  bump(session);
}

export function setInputSessionRaw(session: InputSession, taskId: string, raw: boolean): void {
  session.rawByTask ??= {};
  if (raw) session.rawByTask[taskId] = true;
  else delete session.rawByTask[taskId];
  if ((session.taskId ?? "") === taskId) session.raw = raw;
  bump(session);
}

export function beginQueuedInputEdit(
  session: InputSession,
  taskId: string,
  dispatchId: string,
  text: string,
): void {
  if (session.mode !== "queued-update") session.draftBeforeQueueEdit = session.text;
  session.taskId = taskId;
  session.raw = session.rawByTask?.[taskId] === true;
  session.mode = "queued-update";
  session.queuedDispatchId = dispatchId;
  session.text = text;
  session.phase = "idle";
  session.error = "";
  bump(session);
}

export function cancelQueuedInputEdit(session: InputSession): void {
  if (session.mode !== "queued-update") return;
  session.text = session.draftBeforeQueueEdit ?? "";
  session.mode = "dispatch";
  session.queuedDispatchId = "";
  session.draftBeforeQueueEdit = undefined;
  session.phase = "idle";
  session.error = "";
  bump(session);
}

/** The queue entry disappeared on another device or started running. Keep
 * the edited text as an ordinary draft on the original line. */
export function releaseQueuedInputEdit(session: InputSession): void {
  if (session.mode !== "queued-update") return;
  session.mode = "dispatch";
  session.queuedDispatchId = "";
  session.draftBeforeQueueEdit = undefined;
  session.phase = "idle";
  bump(session);
}

export function clearInputSession(session: InputSession): void {
  session.text = "";
  session.mode = "dispatch";
  session.queuedDispatchId = "";
  session.draftBeforeQueueEdit = undefined;
  session.forceNewSession = false;
  session.phase = "idle";
  session.error = "";
  bump(session);
}

function sameUnchangedIntent(session: InputSession, sent: InputSession): boolean {
  const sameTarget = (session.taskId ?? "") === (sent.taskId ?? "")
    && (session.mode ?? "dispatch") === (sent.mode ?? "dispatch")
    && (session.queuedDispatchId ?? "") === (sent.queuedDispatchId ?? "")
    && (session.raw === true) === (sent.raw === true);
  if (!sameTarget) return false;
  // The backend's draft:sync empty frame may acknowledge the send before its
  // RPC returns. That is still the sent snapshot being cleared, not new work.
  if (session.text === "") return true;
  return (session.revision ?? 0) === (sent.revision ?? 0) && session.text === sent.text;
}

/** Complete only the draft snapshot that was actually sent. New typing or a
 * branch switch while await was pending must survive the completion. */
export function completeInputSessionSend(session: InputSession, sent: InputSession): void {
  session.phase = "idle";
  session.error = "";
  if (!sameUnchangedIntent(session, sent)) return;
  if (sent.mode === "queued-update") cancelQueuedInputEdit(session);
  else clearInputSession(session);
}
