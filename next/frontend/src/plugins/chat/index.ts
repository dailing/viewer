import { defineAsyncComponent, reactive } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { DockInstance, DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import { useLayoutStore } from "../../stores/layout";
import type { ChatList } from "./types";

function createDockProvider(ctx: PluginCtx): DockProvider {
  const layout = useLayoutStore();
  const instances = reactive<DockInstance[]>([]);
  const refresh = async (): Promise<void> => {
    const result = (await ctx.bus.request("chat:_:chats:list", {})) as ChatList;
    instances.splice(0, instances.length, ...result.chats.map((chat) => ({ id: chat.id, label: `${chat.pinned ? "★ " : ""}${chat.name} · ${chat.root}`, state: chat.id === result.active_chat_id ? "running" : undefined, icon: "bi-chat-left-text" })));
  };
  ctx.bus.subscribe("chat:_:active", () => { void refresh(); });
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
