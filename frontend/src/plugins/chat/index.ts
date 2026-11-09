import { defineAsyncComponent, reactive, ref, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { DockAction, DockInstance, DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import { useLayoutStore } from "../../stores/layout";
import { useChatSettingsStore } from "../../stores/chatSettings";
import { registerInputSessionSender, type InputSession } from "../../stores/inputSessions";
import { dockStateFor, isErrorStopReason, markBackground, markChatRead, markTurnCompleted, markTurnStarted, setRunningChats } from "./dockStatus";
import { chatInputRPC } from "./inputDispatch";
import { chatIdForView, createAdditionalChatView, ensureDefaultChatView, ensureTaskView, restoreChatView, type ChatViewParams } from "./viewInstances";
import type { ChatList, DecisionList, Task, TaskList } from "./types";

function sessionChatId(session: InputSession): string {
  const params = session.params as ChatViewParams | undefined;
  return typeof params?.chatId === "string" && params.chatId !== "" ? params.chatId : session.instanceId;
}

/** The Task-first Dock includes every open Task, but only metadata: message
 * history stays inside the Task pane and is never loaded for navigation. */
async function listAllOpenTasks(ctx: PluginCtx): Promise<Task[]> {
  const byId = new Map<string, Task>();
  let cursor: { updatedAt: number; id: string } | undefined;
  for (;;) {
    const request: Record<string, unknown> = { status: "open", limit: 100 };
    if (cursor) {
      request.before_updated_at = cursor.updatedAt;
      request.before_id = cursor.id;
    }
    const result = await ctx.bus.request("chat:_:tasks:list", request) as TaskList;
    for (const task of result.tasks) byId.set(task.id, task);
    if (result.has_more !== true || result.next_updated_at === undefined || !result.next_id) break;
    cursor = { updatedAt: result.next_updated_at, id: result.next_id };
  }
  return [...byId.values()].sort((a, b) =>
    Number(b.running === true) - Number(a.running === true)
    || Number(b.pinned) - Number(a.pinned)
    || b.updated_at - a.updated_at
    || a.id.localeCompare(b.id));
}

function createDockProvider(ctx: PluginCtx): DockProvider {
  const layout = useLayoutStore();
  const chatSettings = useChatSettingsStore();
  const instances = reactive<DockInstance[]>([]);
  let chats: ChatList["chats"] = [];
  let sidebarTasks: Task[] = [];

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
    instances.splice(0, instances.length, ...sidebarTasks.map((task) => ({
      id: ensureTaskView(task.chat_id, task.id),
      label: task.title,
      state: task.running ? "running" : dockStateFor(task.chat_id),
      icon: task.pinned ? "bi-pin-angle-fill" : "bi-file-earmark-text",
      group: {
        id: task.chat_id,
        label: task.chat_name ?? chats.find((chat) => chat.id === task.chat_id)?.name ?? "Chat",
      },
    })));
  };
  const refresh = async (): Promise<void> => {
    const [result, tasks] = await Promise.all([
      ctx.bus.request("chat:_:chats:list", {}) as Promise<ChatList>,
      listAllOpenTasks(ctx),
    ]);
    chats = result.chats;
    sidebarTasks = tasks;
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
  ctx.bus.subscribe("chat:_:task", () => { void refresh(); });
  ctx.bus.subscribe("chat:_:task-summary", () => { void refresh(); });
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
    icon: "bi-folder",
    title: "Chat 分组",
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
    createForGroup: async (chatId) => {
      const result = await ctx.bus.request("chat:_:tasks:create", {
        chat_id: chatId,
        idempotency_key: globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`,
      }) as { task: Task };
      await refresh();
      return ensureTaskView(chatId, result.task.id);
    },
  };
}

function createDecisionActions(ctx: PluginCtx): DockAction[] {
  const pending = ref(0);
  const refresh = async (): Promise<void> => {
    const result = await ctx.bus.request("chat:_:decisions:list", { pending: true, limit: 1, include_pending_count: true }) as DecisionList;
    pending.value = result.pending_count ?? 0;
  };
  ctx.bus.subscribe("chat:_:decision", () => { void refresh().catch(() => undefined); });
  void refresh().catch(() => undefined);
  return [{
    id: "chat-decisions",
    icon: () => "bi bi-inbox",
    title: () => pending.value > 0 ? `Messages / Decisions（${pending.value} 待处理）` : "Messages / Decisions",
    badge: () => pending.value > 99 ? "99+" : pending.value > 0 ? pending.value : "",
    active: () => pending.value > 0,
    onClick: () => { void ctx.shell.open({ paneType: "chat-decisions", instanceId: "main" }); },
  }];
}

export default definePlugin({
  id: "chat",
  name: "聊天",
  components: {
    chat: defineAsyncComponent(() => import("./TaskPane.vue")),
    "chat-tasks": defineAsyncComponent(() => import("./TasksPage.vue")),
    "chat-decisions": defineAsyncComponent(() => import("./DecisionCenter.vue")),
  },
  settings: defineAsyncComponent(() => import("./ChatSettings.vue")),
  createDockProvider,
  createDockActions: createDecisionActions,
  createInstanceAdapters: () => [{
    type: "chat-decisions",
    cardinality: "singleton",
    open() { return "main"; },
  }, {
    type: "chat-tasks",
    cardinality: "singleton",
    open() { return "main"; },
  }, {
    type: "chat",
    cardinality: "multi",
    open(request) {
      if (request.instanceId !== undefined) return restoreChatView(request.instanceId);
      const params = request.params as ChatViewParams | undefined;
      if (typeof params?.chatId !== "string" || params.chatId === "") throw new Error("chatId is required");
      return request.newPane === true
        ? createAdditionalChatView(params)
        : ensureDefaultChatView(params.chatId, params.initialTaskId);
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
