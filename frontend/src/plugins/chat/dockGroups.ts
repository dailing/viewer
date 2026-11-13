import type { DockInstanceGroup } from "../../shell/definePlugin";
import type { Chat, Task } from "./types";

type DockChat = Pick<Chat, "id" | "name" | "pinned">;
type DockTask = Pick<Task, "chat_id" | "chat_name" | "running">;

interface GroupCounts { running: number; idle: number }

/** Chat Settings owns the persistent order. Pinned Chats remain visible without
 * open Tasks; unpinned Chats join the same manual order only while they have one. */
export function chatDockGroups(chats: readonly DockChat[], tasks: readonly DockTask[]): DockInstanceGroup[] {
  const counts = new Map<string, GroupCounts>();
  for (const task of tasks) {
    const count = counts.get(task.chat_id) ?? { running: 0, idle: 0 };
    if (task.running === true) count.running += 1;
    else count.idle += 1;
    counts.set(task.chat_id, count);
  }
  const makeGroup = (id: string, label: string): DockInstanceGroup => {
    const count = counts.get(id) ?? { running: 0, idle: 0 };
    return { id, label, meta: () => `${count.running} running · ${count.idle} idle` };
  };

  const groups = new Map<string, DockInstanceGroup>();
  const taskChatIDs = new Set(tasks.map((task) => task.chat_id));
  for (const chat of chats) {
    if (chat.pinned || taskChatIDs.has(chat.id)) groups.set(chat.id, makeGroup(chat.id, chat.name));
  }
  for (const task of tasks) {
    if (groups.has(task.chat_id)) continue;
    groups.set(task.chat_id, makeGroup(task.chat_id, task.chat_name ?? "Chat"));
  }
  return [...groups.values()];
}
