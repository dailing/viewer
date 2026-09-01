import { defineAsyncComponent, reactive, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { DockInstance, DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import { useLayoutStore } from "../../stores/layout";
import { registerInputSessionSender, type InputSession } from "../../stores/inputSessions";
import { dockStateFor, markChatRead, markTurnCompleted, markTurnStarted, setRunningChats } from "./dockStatus";
import type { ChatList } from "./types";

function createDockProvider(ctx: PluginCtx): DockProvider {
  const layout = useLayoutStore();
  const instances = reactive<DockInstance[]>([]);
  let chats: ChatList["chats"] = [];
  const sync = (): void => {
    // Opening a chat marks it read; clear its unread entry before mapping so
    // the dot disappears in the same rebuild.
    for (const chat of chats) {
      if (layout.isUidOpen(`chat:${chat.id}`)) markChatRead(chat.id);
    }
    instances.splice(0, instances.length, ...chats
      .filter((chat) => chat.pinned || layout.isUidOpen(`chat:${chat.id}`))
      .map((chat) => ({ id: chat.id, label: `${chat.name} · ${chat.root}`, state: dockStateFor(chat.id), icon: "bi-chat-left-text" })));
  };
  const refresh = async (): Promise<void> => {
    const result = (await ctx.bus.request("chat:_:chats:list", {})) as ChatList;
    chats = result.chats;
    setRunningChats(result.running_chat_ids ?? []);
    sync();
  };
  watch(() => layout.panes.map((pane) => pane.content === null ? "" : `${pane.content.paneType}:${pane.content.instanceId}`), sync);
  ctx.bus.subscribe("chat:_:active", () => { void refresh(); });
  // Global turn lifecycle feed: green dot while a turn runs; on completion
  // an amber (unread) or red (failed) dot until the chat is opened.
  ctx.bus.subscribe("chat:_:turn", (frame) => {
    const value = frame.value as { chat_id?: string; phase?: string; stop_reason?: string } | undefined;
    if (value === undefined || typeof value.chat_id !== "string") return;
    if (value.phase === "started") {
      markTurnStarted(value.chat_id);
    } else if (value.phase === "completed") {
      markTurnCompleted(value.chat_id, typeof value.stop_reason === "string" ? value.stop_reason : "", layout.isUidOpen(`chat:${value.chat_id}`));
    } else {
      return;
    }
    sync();
  });
  const handleChatsChanged = (): void => { void refresh(); };
  window.addEventListener("viewer:chats-changed", handleChatsChanged);
  ctx.onDispose(() => window.removeEventListener("viewer:chats-changed", handleChatsChanged));
  void refresh().catch(() => undefined);
  return {
    type: "chat",
    icon: "bi-chat-left-text",
    title: "聊天",
    instances,
    create: async () => {
      const chat = (await ctx.bus.request("chat:_:chats:create", {
        name: "New Chat",
        root: ".",
        type: "group",
        member_role_ids: [],
      })) as { id: string };
      await refresh();
      layout.openInstance("chat", chat.id);
    },
  };
}

export default definePlugin({
  id: "chat",
  components: {
    chat: defineAsyncComponent(() => import("./ChatPane.vue")),
  },
  createDockProvider,
  activate(ctx) {
    const unregister = registerInputSessionSender("chat", async (session: InputSession) => {
      const message = session.text.trim();
      if (!message) return false;
      const payload: Record<string, unknown> = { chat_id: session.instanceId, message };
      if (session.selectedRoleIds.length > 0) payload.role_ids = session.selectedRoleIds;
      if (session.forceNewSession) payload.force_new_session = true;
      if (session.parallel) payload.parallel_dispatch = true;
      // Same as ChatPane.send: outwait LLM role routing (up to
      // llm.timeout_seconds, default 60s) instead of the bus's 30s default.
      await ctx.bus.request("chat:_:dispatch", payload, { timeout: 90_000 });
      return true;
    });
    ctx.onDispose(unregister);
  },
});
