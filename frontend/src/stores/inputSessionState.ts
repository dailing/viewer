export type InputSessionPhase = "idle" | "sending" | "error";
export type InputSessionMode = "dispatch" | "queued-update";

/** The durable meaning of the text, not just the text itself.  A pane may
 * close or change its visible branch while a send is awaiting voice work. */
export interface InputSession {
  id: string;
  pluginId: string;
  paneType: string;
  instanceId: string;
  label: string;
  text: string;
  branchId: string;
  mode: InputSessionMode;
  queuedDispatchId: string;
  draftBeforeQueueEdit?: string;
  revision: number;
  selectedRoleIds: string[];
  forceNewSession: boolean;
  parallel: boolean;
  pinned: boolean;
  phase: InputSessionPhase;
  error: string;
  updatedAt: number;
}

export interface InputSessionIntent {
  branchId: string;
  mode: InputSessionMode;
  queuedDispatchId: string;
  draftBeforeQueueEdit?: string;
  selectedRoleIds: string[];
  forceNewSession: boolean;
  parallel: boolean;
}

function bump(session: InputSession): void {
  session.revision = (session.revision ?? 0) + 1;
  session.updatedAt = Date.now();
}

export function inputSessionIntent(session: InputSession): InputSessionIntent {
  return {
    branchId: session.branchId ?? "",
    mode: session.mode ?? "dispatch",
    queuedDispatchId: session.queuedDispatchId ?? "",
    draftBeforeQueueEdit: session.draftBeforeQueueEdit,
    selectedRoleIds: [...session.selectedRoleIds],
    forceNewSession: session.forceNewSession,
    parallel: session.parallel,
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

export function setInputSessionBranch(session: InputSession, branchId: string): void {
  if ((session.branchId ?? "") === branchId) return;
  // A queued edit belongs to its original entry even if the visible tabs
  // change. Restore the ordinary draft before re-binding the input.
  if (session.mode === "queued-update") cancelQueuedInputEdit(session);
  session.branchId = branchId;
  bump(session);
}

export function beginQueuedInputEdit(
  session: InputSession,
  branchId: string,
  dispatchId: string,
  text: string,
): void {
  if (session.mode !== "queued-update") session.draftBeforeQueueEdit = session.text;
  session.branchId = branchId;
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
  session.parallel = false;
  session.phase = "idle";
  session.error = "";
  bump(session);
}

function sameUnchangedIntent(session: InputSession, sent: InputSession): boolean {
  const sameTarget = (session.branchId ?? "") === (sent.branchId ?? "")
    && (session.mode ?? "dispatch") === (sent.mode ?? "dispatch")
    && (session.queuedDispatchId ?? "") === (sent.queuedDispatchId ?? "");
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
