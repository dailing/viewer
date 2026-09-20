import { defineAsyncComponent, reactive, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { DockInstance, DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import { useLayoutStore } from "../../stores/layout";
import { useChatSettingsStore } from "../../stores/chatSettings";
import { registerInputSessionSender, type InputSession } from "../../stores/inputSessions";
import { dockStateFor, isErrorStopReason, markChatRead, markTurnCompleted, markTurnStarted, setRunningChats } from "./dockStatus";
import type { ChatList } from "./types";

function createDockProvider(ctx: PluginCtx): DockProvider {
  const layout = useLayoutStore();
  const chatSettings = useChatSettingsStore();
  const instances = reactive<DockInstance[]>([]);
  let chats: ChatList["chats"] = [];

  /** System notification on turn completion (framework v0.79): fires only
   *  while the viewer tab is hidden (an in-view completion is already
   *  covered by the dock dot) and only with the setting on + permission
   *  granted. The per-chat tag coalesces a multi-role batch into one
   *  notification; clicking focuses the window and opens the chat. */
  const notifyTurnCompleted = (chatId: string, roleName: string | undefined, stopReason: string): void => {
    if (!chatSettings.turnNotifications) return;
    if (!("Notification" in window) || Notification.permission !== "granted") return;
    if (!document.hidden) return;
    const chat = chats.find((item) => item.id === chatId);
    const failed = isErrorStopReason(stopReason);
    const body = `${roleName || "角色"} · ${failed ? `失败：${stopReason}` : "一轮完成"}`;
    const notification = new Notification(chat?.name ?? "聊天", { body, tag: `chat-turn:${chatId}` });
    notification.onclick = () => {
      window.focus();
      layout.openInstance("chat", chatId);
    };
  };
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
    const value = frame.value as { chat_id?: string; phase?: string; stop_reason?: string; role_name?: string } | undefined;
    if (value === undefined || typeof value.chat_id !== "string") return;
    if (value.phase === "started") {
      markTurnStarted(value.chat_id);
    } else if (value.phase === "completed") {
      const stopReason = typeof value.stop_reason === "string" ? value.stop_reason : "";
      markTurnCompleted(value.chat_id, stopReason, layout.isUidOpen(`chat:${value.chat_id}`));
      notifyTurnCompleted(value.chat_id, value.role_name, stopReason);
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
