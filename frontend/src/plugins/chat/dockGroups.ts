import type { DockInstanceGroup } from "../../shell/definePlugin";
import type { Chat, Task } from "./types";

type DockChat = Pick<Chat, "id" | "name" | "pinned">;
type DockTask = Pick<Task, "chat_id" | "chat_name">;

/** Chat Settings owns the persistent order. Pinned Chats remain visible without
 * open Tasks; unpinned Chats join the same manual order only while they have one. */
export function chatDockGroups(chats: readonly DockChat[], tasks: readonly DockTask[]): DockInstanceGroup[] {
  const groups = new Map<string, DockInstanceGroup>();
  const taskChatIDs = new Set(tasks.map((task) => task.chat_id));
  for (const chat of chats) {
    if (chat.pinned || taskChatIDs.has(chat.id)) groups.set(chat.id, { id: chat.id, label: chat.name });
  }
  for (const task of tasks) {
    if (groups.has(task.chat_id)) continue;
    groups.set(task.chat_id, { id: task.chat_id, label: task.chat_name ?? "Chat" });
  }
  return [...groups.values()];
}
