/** Pure chat timeline window operations. No Vue, network, or DOM state. */
import type { MessageCursor } from "./chatCache";

export interface WindowMessage {
  id: string;
  created_at: number;
}

export interface ChatWindow<T extends WindowMessage> {
  messages: T[];
  hasOlder: boolean;
  olderCursor: MessageCursor | null;
  loadedLo: number;
  hasNewer: boolean;
  loadedHi: MessageCursor | null;
}

export interface DeltaPage<T extends WindowMessage> {
  messages: T[];
  hasMore: boolean;
}

export function cursorOf(message: WindowMessage | null | undefined): MessageCursor | null {
  return message ? { ts: message.created_at, id: message.id } : null;
}

export function compareMessages(a: WindowMessage, b: WindowMessage): number {
  return a.created_at - b.created_at || a.id.localeCompare(b.id);
}

/** Upsert by id and restore the composite-cursor order. Incoming rows replace
 * cached rows because a streaming message keeps its id while its text grows. */
export function mergeMessages<T extends WindowMessage>(current: T[], incoming: T[]): T[] {
  const byId = new Map(current.map((item) => [item.id, item] as const));
  for (const item of incoming) byId.set(item.id, item);
  return [...byId.values()].sort(compareMessages);
}

/** Fetch at most maxPages after an inclusive cursor. `incomplete` is explicit:
 * callers must not mistake a capped catch-up for the live edge. */
export async function collectDeltaPages<T extends WindowMessage>(
  initialCursor: MessageCursor | null,
  fetchPage: (cursor: MessageCursor | null) => Promise<DeltaPage<T>>,
  maxPages = 3,
): Promise<{ pages: T[][]; incomplete: boolean }> {
  const pages: T[][] = [];
  let cursor = initialCursor;
  for (let pageCount = 0; pageCount < maxPages; pageCount++) {
    const page = await fetchPage(cursor);
    pages.push(page.messages);
    if (initialCursor === null || page.messages.length === 0 || !page.hasMore) {
      return { pages, incomplete: false };
    }
    cursor = cursorOf(page.messages[page.messages.length - 1]);
    if (pageCount === maxPages - 1) return { pages, incomplete: true };
  }
  return { pages, incomplete: false };
}

export function evictTop<T extends WindowMessage>(window: ChatWindow<T>, pageSize: number): ChatWindow<T> {
  if (window.messages.length === 0) return window;
  const messages = window.messages.slice(Math.min(pageSize, window.messages.length));
  const oldest = messages[0];
  return {
    ...window,
    messages,
    loadedLo: oldest?.created_at ?? 0,
    olderCursor: cursorOf(oldest),
    hasOlder: true,
  };
}

export function evictBottom<T extends WindowMessage>(window: ChatWindow<T>, pageSize: number): ChatWindow<T> {
  if (window.messages.length === 0) return window;
  const messages = window.messages.slice(0, Math.max(0, window.messages.length - pageSize));
  return {
    ...window,
    messages,
    loadedHi: cursorOf(messages[messages.length - 1]),
    hasNewer: true,
  };
}

/** Bound the resident timeline. Attached windows always shed the top; detached
 * windows follow browse intent and may shed the bottom while reading upward. */
export function trimWindow<T extends WindowMessage>(
  initial: ChatWindow<T>,
  maxMessages: number,
  pageSize: number,
  prefer: "top" | "bottom",
): ChatWindow<T> {
  let window = initial;
  while (window.messages.length > maxMessages) {
    window = !window.hasNewer || prefer === "top" ? evictTop(window, pageSize) : evictBottom(window, pageSize);
  }
  return window;
}

export function beyondWindowEdge(window: Pick<ChatWindow<WindowMessage>, "hasNewer" | "loadedHi">, ts: number, id: string): boolean {
  const hi = window.loadedHi;
  if (!window.hasNewer || hi === null) return false;
  return ts > hi.ts || (ts === hi.ts && id > hi.id);
}
