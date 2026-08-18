import { defineAsyncComponent, reactive, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { DockInstance, DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import { useLayoutStore } from "../../stores/layout";
import type { ChatList } from "./types";

function createDockProvider(ctx: PluginCtx): DockProvider {
  const layout = useLayoutStore();
  const instances = reactive<DockInstance[]>([]);
  let chats: ChatList["chats"] = [];
  let activeChatId = "";
  const sync = (): void => {
    instances.splice(0, instances.length, ...chats
      .filter((chat) => chat.pinned || layout.isUidOpen(`chat:${chat.id}`))
      .map((chat) => ({ id: chat.id, label: `${chat.name} · ${chat.root}`, state: chat.id === activeChatId ? "running" : undefined, icon: "bi-chat-left-text" })));
  };
  const refresh = async (): Promise<void> => {
    const result = (await ctx.bus.request("chat:_:chats:list", {})) as ChatList;
    chats = result.chats;
    activeChatId = result.active_chat_id;
    sync();
  };
  watch(() => layout.panes.map((pane) => pane.content === null ? "" : `${pane.content.paneType}:${pane.content.instanceId}`), sync);
  ctx.bus.subscribe("chat:_:active", () => { void refresh(); });
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
});
