import type { ActivityTimelineItem } from "./toolPresentation";
import type { ChatMessage, Decision } from "./types";

export interface ChatTimelineItem extends ActivityTimelineItem {
  message?: ChatMessage;
}

export function visibleTurnItems(items: ChatTimelineItem[], showActivities: boolean): ChatTimelineItem[] {
  return showActivities ? items : items.filter((item) => item.kind === "message");
}

/** Render the same visible assistant text in Task and Decision context views. */
export function visibleMessageText(message: ChatMessage, decisions: Decision[]): string {
  const decision = decisions.find((item) => item.source_message_id === message.id);
  if (!decision || decision.status === "invalid") return message.text;
  const bytes = new TextEncoder().encode(message.text);
  const decoder = new TextDecoder();
  return `${decoder.decode(bytes.slice(0, decision.marker_start))}${decoder.decode(bytes.slice(decision.marker_end))}`;
}
