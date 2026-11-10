import type { InputSession } from "../../stores/inputSessions";
import { reportFrontendLog } from "../../shell/frontendLog";

function chatId(session: InputSession): string {
  const params = session.params as { chatId?: unknown } | undefined;
  if (typeof params?.chatId === "string" && params.chatId !== "") return params.chatId;
  // Legacy sessions used the plain chat id as their instance id. Composite
  // view ids are intentionally not parsed; new sessions always carry params.
  return session.instanceId;
}

/** Build the pane-independent RPC for a durable input intent. Closing the
 * pane therefore changes neither queue-edit semantics nor line/raw routing. */
export function chatInputRPC(session: InputSession, memberRoleIds?: readonly string[]): { channel: string; payload: Record<string, unknown> } {
  const targetChatId = chatId(session);
  const message = session.text.trim();
  if (session.mode === "queued-update" && session.queuedDispatchId) {
    return {
      channel: "chat:_:queued-update",
      payload: { chat_id: targetChatId, dispatch_id: session.queuedDispatchId, message },
    };
  }
  const taskId = session.taskId ?? "";
  const payload: Record<string, unknown> = {
    chat_id: targetChatId,
    input_instance_id: session.instanceId,
    text: message,
    task_id: taskId,
    // A human send explicitly continues an ended Task. Programmatic and queued
    // edits keep the ordinary open-Task boundary unless they opt in separately.
    reopen_ended: true,
    idempotency_key: `${session.instanceId}:${session.revision}`,
  };
  const selectedRoleIds = memberRoleIds === undefined
    ? session.selectedRoleIds
    : session.selectedRoleIds.filter((id) => memberRoleIds.includes(id));
  if (memberRoleIds !== undefined && selectedRoleIds.length !== session.selectedRoleIds.length) {
    const message = "[chat] dispatch dropped non-member role ids";
    const context = {
      chatId: targetChatId,
      droppedRoleIds: session.selectedRoleIds.filter((id) => !memberRoleIds.includes(id)),
      selectedRoleIds: [...session.selectedRoleIds],
      memberRoleIds: [...memberRoleIds],
    };
    console.warn(message, context);
    reportFrontendLog("warn", "chat", message, context);
  }
  if (selectedRoleIds.length > 0) payload.role_ids = [...selectedRoleIds];
  if (session.forceNewSession) payload.force_new_session = true;
  if (session.raw) payload.raw = true;
  return { channel: "chat:_:send-message", payload };
}
