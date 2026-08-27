import { useInputSessionsStore } from "../../stores/inputSessions";
import { useVoiceStore } from "./voiceStore";

/** Finish any in-flight recording/refine before dispatching its bound input.
 * This is shared by the inline composer and the workspace bar, so closing a
 * pane cannot bypass the final voice-service refinement step. */
export async function sendInputSession(id: string): Promise<boolean> {
  const voice = useVoiceStore();
  const input = useInputSessionsStore();
  let text = input.session(id)?.text ?? "";
  const state = voice.context(id);
  if (["connecting", "recording", "processing"].includes(state.status)) {
    text = await voice.finishForSend(id);
  } else if (state.text.trim()) {
    text = state.text;
  }
  input.setText(id, text);
  const sent = await input.send(id, text);
  if (sent) voice.clear(id);
  return sent;
}

