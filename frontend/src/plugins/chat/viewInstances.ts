import { reactive } from "vue";

export interface ChatViewParams {
  chatId: string;
  initialTabs?: string[];
}

export interface ChatViewInstance extends ChatViewParams {
  id: string;
  defaultForChat: boolean;
}

const STORAGE_KEY = "viewer.chatViews.v1";

function load(): Record<string, ChatViewInstance> {
  try {
    const parsed = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "{}") as Record<string, ChatViewInstance>;
    return Object.fromEntries(Object.entries(parsed).filter(([, value]) =>
      value !== null && typeof value.id === "string" && typeof value.chatId === "string" && value.chatId !== "",
    ));
  } catch {
    return {};
  }
}

const views = reactive<Record<string, ChatViewInstance>>(load());

function persist(): void {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(views));
}

function newId(): string {
  return globalThis.crypto?.randomUUID?.() ?? `view-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

function create(params: ChatViewParams, defaultForChat: boolean): ChatViewInstance {
  if (params.chatId === "") throw new Error("chatId is required");
  const view: ChatViewInstance = {
    id: newId(),
    chatId: params.chatId,
    initialTabs: params.initialTabs === undefined ? undefined : [...params.initialTabs],
    defaultForChat,
  };
  views[view.id] = view;
  persist();
  return view;
}

/** The pinned/default Dock target for a Chat; repeated opens reuse it. */
export function ensureDefaultChatView(chatId: string): string {
  const existing = Object.values(views).find((view) => view.chatId === chatId && view.defaultForChat);
  return existing?.id ?? create({ chatId }, true).id;
}

/** A separate view+input pair, optionally seeded with the source view's tabs. */
export function createAdditionalChatView(params: ChatViewParams): string {
  return create(params, false).id;
}

export function resolveChatView(instanceId: string): ChatViewInstance | undefined {
  return views[instanceId];
}

/** Legacy plain chat ids remain acceptable at the RPC boundary during migration. */
export function chatIdForView(instanceId: string): string {
  return views[instanceId]?.chatId ?? instanceId;
}

/** Restore a view id, or adopt a legacy plain chat id as its default view. */
export function restoreChatView(instanceId: string): string {
  if (views[instanceId] !== undefined) return instanceId;
  return ensureDefaultChatView(instanceId);
}

export function removeChatViews(chatId: string): void {
  let changed = false;
  for (const [id, view] of Object.entries(views)) {
    if (view.chatId !== chatId) continue;
    delete views[id];
    changed = true;
  }
  if (changed) persist();
}

export function clearChatViewsForTest(): void {
  for (const id of Object.keys(views)) delete views[id];
}
