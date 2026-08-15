/**
 * Session-scoped chat cache (framework v0.32, A.7).
 *
 * The ChatPane unmounts when its dock instance closes, so loaded history lives
 * here — module-level, outside any component. Reopening a chat hydrates
 * instantly from the cache and merges only the delta: messages at-or-newer
 * than the cached newest (via the `after`/`after_id` cursor, inclusive
 * boundary so a still-streaming row's final text replaces the cached copy)
 * and blocks newer than the cached max occurred_at. Repeat opens with nothing
 * new cost a few KiB instead of a full page + window refetch; already-loaded
 * older pages cost zero.
 *
 * Memory bounds: LRU across chats (Map insertion order, touched on load/save)
 * plus per-chat caps. Trimming drops the oldest half and resets the older
 * pagination boundary so scrolling to the top transparently re-fetches.
 */
import type { Chat, ChatBlock, ChatMessage, Role, Workspace } from "./types";

export interface MessageCursor {
  ts: number;
  id: string;
}

export interface ChatCacheEntry {
  chat: Chat | null;
  /** Ascending by (created_at, id) — the loaded span. */
  messages: ChatMessage[];
  /** Ascending by (occurred_at, id) — the loaded span. */
  blocks: ChatBlock[];
  roles: Role[];
  workspace: Workspace | null;
  hasOlder: boolean;
  olderCursor: MessageCursor | null;
  /** Oldest loaded message created_at; 0 = nothing loaded below the top page. */
  loadedLo: number;
  /** Max occurred_at among loaded blocks (delta fetch boundary). */
  blockHigh: number;
}

const MAX_CHATS = 24;
const MAX_MESSAGES = 2000;
const MAX_BLOCKS = 4000;

const entries = new Map<string, ChatCacheEntry>();

function touch(chatId: string): void {
  const entry = entries.get(chatId);
  if (!entry) return;
  entries.delete(chatId);
  entries.set(chatId, entry);
  while (entries.size > MAX_CHATS) {
    entries.delete(entries.keys().next().value as string);
  }
}

/** Drop the oldest half of an over-cap entry and reset the older-pagination
 *  boundary so a later scroll to the top transparently re-fetches. */
function trim(entry: ChatCacheEntry): void {
  if (entry.messages.length > MAX_MESSAGES) {
    entry.messages.splice(0, entry.messages.length - MAX_MESSAGES);
    const oldest = entry.messages[0];
    entry.hasOlder = true;
    entry.loadedLo = oldest?.created_at ?? 0;
    entry.olderCursor = oldest ? { ts: oldest.created_at, id: oldest.id } : null;
  }
  if (entry.blocks.length > MAX_BLOCKS) {
    entry.blocks.splice(0, entry.blocks.length - MAX_BLOCKS);
  }
}

export function loadEntry(chatId: string): ChatCacheEntry | undefined {
  const entry = entries.get(chatId);
  if (!entry) return undefined;
  touch(chatId);
  return entry;
}

export function saveEntry(chatId: string, entry: ChatCacheEntry): void {
  trim(entry);
  entries.set(chatId, entry);
  touch(chatId);
}

export function removeEntry(chatId: string): void {
  entries.delete(chatId);
}
