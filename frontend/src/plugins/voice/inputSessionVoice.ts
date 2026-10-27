import { useInputSessionsStore } from "../../stores/inputSessions";
import { useVoiceStore } from "./voiceStore";

/** Finish any in-flight recording/refine before dispatching its bound input.
 * This is shared by the inline composer and the workspace bar, so closing a
 * pane cannot bypass the final voice-service refinement step. */
export async function sendInputSession(id: string): Promise<boolean> {
  const voice = useVoiceStore();
  const input = useInputSessionsStore();
  const session = input.session(id);
  if (session === undefined) return false;
  // Bind the destination before recording/refinement can await. The visible
  // branch may change while voice work is in flight; it must not retarget it.
  const intent = input.intent(id);
  if (intent === undefined) return false;
  let text = session.text;
  const state = voice.context(id);
  if (["connecting", "recording", "processing"].includes(state.status)) {
    text = await voice.finishForSend(id);
  } else if (state.text.trim()) {
    text = state.text;
  }
  const current = input.session(id);
  if (current !== undefined
    && (current.branchId ?? "") === intent.branchId
    && current.mode === intent.mode
    && current.queuedDispatchId === intent.queuedDispatchId) {
    input.setText(id, text);
  }
  const sent = await input.send(id, text, intent);
  if (sent) voice.clear(id);
  return sent;
}

