import { defineAsyncComponent, reactive, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { DockInstance, DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import { useLayoutStore } from "../../stores/layout";
import { useChatSettingsStore } from "../../stores/chatSettings";
import { registerInputSessionSender, type InputSession } from "../../stores/inputSessions";
import { dockStateFor, isErrorStopReason, markBackground, markChatRead, markTurnCompleted, markTurnStarted, setRunningChats } from "./dockStatus";
import { chatInputRPC } from "./inputDispatch";
import { chatIdForView, createAdditionalChatView, ensureDefaultChatView, restoreChatView, type ChatViewParams } from "./viewInstances";
import type { ChatList } from "./types";

function sessionChatId(session: InputSession): string {
  const params = session.params as ChatViewParams | undefined;
  return typeof params?.chatId === "string" && params.chatId !== "" ? params.chatId : session.instanceId;
}

function createDockProvider(ctx: PluginCtx): DockProvider {
  const layout = useLayoutStore();
  const chatSettings = useChatSettingsStore();
  const instances = reactive<DockInstance[]>([]);
  let chats: ChatList["chats"] = [];

  const isChatOpen = (chatId: string): boolean => {
    const matches = (content: { paneType: string; instanceId: string } | null): boolean =>
      content?.paneType === "chat" && chatIdForView(content.instanceId) === chatId;
    return layout.panes.some((pane) => matches(pane.content)) || layout.floating.some((pane) => matches(pane.content));
  };

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
      void ctx.shell.open({ paneType: "chat", instanceId: ensureDefaultChatView(chatId) });
    };
  };
  const sync = (): void => {
    for (const chat of chats) {
      if (isChatOpen(chat.id)) markChatRead(chat.id);
    }
    instances.splice(0, instances.length, ...chats
      .filter((chat) => chat.pinned)
      .map((chat) => ({
        id: ensureDefaultChatView(chat.id),
        label: `${chat.name} · ${chat.root}`,
        state: dockStateFor(chat.id),
        icon: "bi-chat-left-text",
      })));
  };
  const refresh = async (): Promise<void> => {
    const result = (await ctx.bus.request("chat:_:chats:list", {})) as ChatList;
    chats = result.chats;
    setRunningChats(result.running_chat_ids ?? []);
    sync();
  };
  watch(
    () => [
      ...layout.panes.map((pane) => pane.content === null ? "" : `${pane.content.paneType}:${pane.content.instanceId}`),
      ...layout.floating.map((pane) => `${pane.content.paneType}:${pane.content.instanceId}`),
    ],
    sync,
  );
  ctx.bus.subscribe("chat:_:active", () => { void refresh(); });
  ctx.bus.subscribe("chat:_:turn", (frame) => {
    const value = frame.value as { chat_id?: string; phase?: string; stop_reason?: string; role_name?: string } | undefined;
    if (value === undefined || typeof value.chat_id !== "string") return;
    if (value.phase === "started") {
      markTurnStarted(value.chat_id);
    } else if (value.phase === "completed") {
      const stopReason = typeof value.stop_reason === "string" ? value.stop_reason : "";
      markTurnCompleted(value.chat_id, stopReason, isChatOpen(value.chat_id));
      notifyTurnCompleted(value.chat_id, value.role_name, stopReason);
    } else {
      return;
    }
    sync();
  });
  ctx.bus.subscribe("chat:_:background", (frame) => {
    const value = frame.value as { chat_id?: string; active?: boolean } | undefined;
    if (value === undefined || typeof value.chat_id !== "string") return;
    markBackground(value.chat_id, value.active === true);
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
      return ensureDefaultChatView(chat.id);
    },
  };
}

export default definePlugin({
  id: "chat",
  name: "聊天",
  components: {
    chat: defineAsyncComponent(() => import("./ChatPane.vue")),
  },
  settings: defineAsyncComponent(() => import("./ChatSettings.vue")),
  createDockProvider,
  createInstanceAdapters: () => [{
    type: "chat",
    cardinality: "multi",
    open(request) {
      if (request.instanceId !== undefined) return restoreChatView(request.instanceId);
      const params = request.params as ChatViewParams | undefined;
      if (typeof params?.chatId !== "string" || params.chatId === "") throw new Error("chatId is required");
      return request.newPane === true
        ? createAdditionalChatView(params)
        : ensureDefaultChatView(params.chatId);
    },
  }],
  activate(ctx) {
    const unregister = registerInputSessionSender("chat", async (session: InputSession) => {
      const message = session.text.trim();
      if (!message) return false;
      const chatId = sessionChatId(session);
      let memberRoleIds: string[] | undefined;
      if (session.selectedRoleIds.length > 0) {
        const chats = (await ctx.bus.request("chat:_:chats:list", {})) as ChatList;
        memberRoleIds = chats.chats.find((chat) => chat.id === chatId)?.member_role_ids;
      }
      const request = chatInputRPC(session, memberRoleIds);
      const result = await ctx.bus.request(request.channel, request.payload, { timeout: 90_000 });
      return (result as { status?: string } | undefined)?.status !== "not_queued";
    });
    ctx.onDispose(unregister);
  },
});
