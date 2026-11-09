export interface ReadingAnchor {
  createdAt: number;
  messageId: string;
}

export interface HistoryMessage {
  id: string;
  created_at: number;
  deleted?: boolean;
}

export const MESSAGE_WINDOW_LIMIT = 300;

/** Keep one ordered, bounded window per Task. Eviction is always from the old
 * edge, so streaming newer messages cannot silently make older history
 * unreachable. */
export function mergeMessageWindow<T extends HistoryMessage>(current: readonly T[], incoming: readonly T[], limit = MESSAGE_WINDOW_LIMIT): { messages: T[]; evictedOlder: boolean } {
  const byId = new Map(current.map((message) => [message.id, message]));
  for (const message of incoming) {
    if (message.deleted) byId.delete(message.id);
    else byId.set(message.id, message);
  }
  const ordered = [...byId.values()].sort((a, b) => a.created_at - b.created_at || a.id.localeCompare(b.id));
  return { messages: ordered.slice(-limit), evictedOlder: ordered.length > limit };
}

export function readingAnchor(value: unknown, taskId: string): ReadingAnchor | undefined {
  if (!taskId || value === null || typeof value !== "object") return undefined;
  const candidate = (value as Record<string, unknown>)[taskId];
  if (candidate === null || typeof candidate !== "object") return undefined;
  const anchor = candidate as Record<string, unknown>;
  return typeof anchor.createdAt === "number" && Number.isFinite(anchor.createdAt) && anchor.createdAt > 0 && typeof anchor.messageId === "string" && anchor.messageId !== ""
    ? { createdAt: anchor.createdAt, messageId: anchor.messageId }
    : undefined;
}

export function withReadingAnchor(value: unknown, taskId: string, anchor: ReadingAnchor): Record<string, ReadingAnchor> {
  const existing = value !== null && typeof value === "object" ? value as Record<string, ReadingAnchor> : {};
  return { ...existing, [taskId]: anchor };
}

export function storeTaskReadingAnchor(viewKey: string, taskId: string, anchor: ReadingAnchor): void {
  const key = `viewer:chat:${viewKey}:readingAnchors`;
  let current: unknown = {};
  try { current = JSON.parse(localStorage.getItem(key) ?? "{}") as unknown; } catch { current = {}; }
  localStorage.setItem(key, JSON.stringify(withReadingAnchor(current, taskId, anchor)));
}
