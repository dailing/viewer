import type { InputSession } from "../../stores/inputSessions";
import { reportFrontendLog } from "../../shell/frontendLog";

/** Build the pane-independent RPC for a durable input intent. Closing the
 * pane therefore changes neither queue-edit semantics nor line/raw routing. */
export function chatInputRPC(session: InputSession, raw: boolean, memberRoleIds?: readonly string[]): { channel: string; payload: Record<string, unknown> } {
  const chatId = session.instanceId.split("#")[0];
  const message = session.text.trim();
  if (session.mode === "queued-update" && session.queuedDispatchId) {
    return {
      channel: "chat:_:queued-update",
      payload: { chat_id: chatId, dispatch_id: session.queuedDispatchId, message },
    };
  }
  const branchId = session.branchId ?? "";
  const payload: Record<string, unknown> = { chat_id: chatId, message, branch_id: branchId };
  const selectedRoleIds = memberRoleIds === undefined
    ? session.selectedRoleIds
    : session.selectedRoleIds.filter((id) => memberRoleIds.includes(id));
  if (memberRoleIds !== undefined && selectedRoleIds.length !== session.selectedRoleIds.length) {
    const message = "[chat] dispatch dropped non-member role ids";
    const context = {
      chatId,
      droppedRoleIds: session.selectedRoleIds.filter((id) => !memberRoleIds.includes(id)),
      selectedRoleIds: [...session.selectedRoleIds],
      memberRoleIds: [...memberRoleIds],
    };
    console.warn(message, context);
    reportFrontendLog("warn", "chat", message, context);
  }
  if (selectedRoleIds.length > 0) payload.role_ids = [...selectedRoleIds];
  if (session.forceNewSession) payload.force_new_session = true;
  if (session.parallel) payload.parallel_dispatch = true;
  if (raw) payload.raw = true;
  return { channel: "chat:_:dispatch", payload };
}
