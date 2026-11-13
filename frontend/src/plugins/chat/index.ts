import { defineAsyncComponent, reactive, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { DockInstance, DockInstanceGroup, DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import { useLayoutStore } from "../../stores/layout";
import { useChatSettingsStore } from "../../stores/chatSettings";
import { registerInputSessionSender, type InputSession } from "../../stores/inputSessions";
import { dockUnreadStateFor, isErrorStopReason, markTaskRead, markTurnCompleted } from "./dockStatus";
import { chatDockGroups } from "./dockGroups";
import { chatInputRPC } from "./inputDispatch";
import { chatIdForView, createAdditionalChatView, ensureDefaultChatView, ensureTaskView, resolveChatView, restoreChatView, type ChatViewParams } from "./viewInstances";
import type { ChatList, Task, TaskList } from "./types";

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
  const groups = reactive<DockInstanceGroup[]>([]);
  let chats: ChatList["chats"] = [];
  let sidebarTasks: Task[] = [];

  const isTaskOpen = (chatId: string, taskId: string): boolean => {
    const matches = (content: { paneType: string; instanceId: string } | null): boolean =>
      content?.paneType === "chat" && chatIdForView(content.instanceId) === chatId && resolveChatView(content.instanceId)?.initialTaskId === taskId;
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
    for (const task of sidebarTasks) {
      if (isTaskOpen(task.chat_id, task.id)) markTaskRead(task.id);
    }
    groups.splice(0, groups.length, ...chatDockGroups(chats, sidebarTasks));
    instances.splice(0, instances.length, ...sidebarTasks.map((task) => ({
      id: ensureTaskView(task.chat_id, task.id),
      label: task.title,
      state: task.running ? "running" : dockUnreadStateFor(task.id),
      icon: task.pinned ? "bi-pin-angle-fill" : "bi-chat-left-text",
      group: {
        id: task.chat_id,
        label: task.chat_name ?? chats.find((chat) => chat.id === task.chat_id)?.name ?? "Chat",
      },
    })));
  };
  let refreshGeneration = 0;
  const refresh = async (): Promise<void> => {
    const generation = ++refreshGeneration;
    const [result, tasks] = await Promise.all([
      ctx.bus.request("chat:_:chats:list", {}) as Promise<ChatList>,
      listAllOpenTasks(ctx),
    ]);
    if (generation !== refreshGeneration) return;
    chats = result.chats;
    sidebarTasks = tasks;
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
    const value = frame.value as { chat_id?: string; task_id?: string; phase?: string; stop_reason?: string; role_name?: string } | undefined;
    if (value === undefined || typeof value.chat_id !== "string") return;
    if (value.phase !== "started" && value.phase !== "completed") return;
    if (value.phase === "completed") {
      const stopReason = typeof value.stop_reason === "string" ? value.stop_reason : "";
      if (typeof value.task_id === "string") markTurnCompleted(value.task_id, stopReason, isTaskOpen(value.chat_id, value.task_id));
      notifyTurnCompleted(value.chat_id, value.role_name, stopReason);
    }
    void refresh().catch(() => undefined);
  });
  ctx.bus.subscribe("chat:_:background", () => { void refresh().catch(() => undefined); });
  const handleChatsChanged = (): void => { void refresh(); };
  const handleTaskCurrent = (event: Event): void => {
    const taskId = (event as CustomEvent<{ taskId?: string }>).detail?.taskId;
    if (taskId) markTaskRead(taskId);
    sync();
  };
  window.addEventListener("viewer:chats-changed", handleChatsChanged);
  window.addEventListener("viewer:task-current", handleTaskCurrent);
  ctx.onDispose(() => {
    window.removeEventListener("viewer:chats-changed", handleChatsChanged);
    window.removeEventListener("viewer:task-current", handleTaskCurrent);
  });
  void refresh().catch(() => undefined);
  return {
    type: "chat",
    icon: "bi-chat-left-text",
    title: "Chat 分组",
    groups,
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

export default definePlugin({
  id: "chat",
  name: "聊天",
  components: {
    chat: defineAsyncComponent(() => import("./TaskPane.vue")),
    "chat-tasks": defineAsyncComponent(() => import("./TasksPage.vue")),
  },
  settings: defineAsyncComponent(() => import("./ChatSettings.vue")),
  createDockProvider,
  createInstanceAdapters: () => [{
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
