import { visibleMessageText, type ChatTimelineItem } from "../chat/turnContent";
import type { ChatMessage, Decision } from "../chat/types";

function joinAssistantText(left: string, right: string): string {
  const head = left.trimEnd();
  const tail = right.trimStart();
  if (!head) return tail;
  if (!tail) return head;
  return `${head}\n\n${tail}`;
}

/** Strip each source marker before joining assistant fragments into one Turn body. */
export function decisionContextItems(messages: ChatMessage[], decision: Decision): ChatTimelineItem[] {
  const merged: ChatMessage[] = [];
  for (const source of messages) {
    const message = { ...source, text: visibleMessageText(source, [decision]) };
    const previous = merged[merged.length - 1];
    if (message.role === "assistant" && previous?.role === "assistant") {
      previous.text = joinAssistantText(previous.text, message.text);
      previous.created_at = message.created_at;
      continue;
    }
    merged.push(message);
  }
  return merged.map((message) => ({ id: message.id, at: message.created_at, kind: "message", message }));
}

export function decisionStatusLabel(decision: Decision): string {
  if (decision.status === "resolved") return "finished";
  if (decision.status === "invalid") return "error";
  return decision.status;
}

export function decisionStatusIcon(decision: Decision): string {
  if (decision.status === "resolved") return "bi-check2-circle";
  if (decision.status === "invalid") return "bi-exclamation-triangle";
  if (decision.status === "cancelled") return "bi-slash-circle";
  return "bi-hourglass-split";
}
