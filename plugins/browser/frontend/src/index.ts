import { defineAsyncComponent, reactive } from "vue";
import { definePlugin, type DockInstance, type PluginCtx } from "@viewer/bus-sdk";

interface BrowserPage {
  page_id: string;
  title: string;
  url: string;
  online: boolean;
  busy: boolean;
  watchers: number;
  control_owner: string;
}

interface BrowserState {
  backend: string;
  pages: BrowserPage[];
}

function createDockProvider(ctx: PluginCtx) {
  const instances = reactive<DockInstance[]>([]);
  function sync(state: BrowserState): void {
    instances.splice(0, instances.length, ...(state.pages ?? []).filter((page) => page.watchers > 0).map((page) => ({
      id: page.page_id,
      label: `${page.title || page.url || "Browser"} · ${page.page_id}`,
      state: !page.online ? "error" : page.control_owner === "agent" ? "running" : "idle",
      icon: "bi-window-desktop",
    })));
  }
  ctx.bus.subscribe("viewer.browser:_:state", (frame) => sync(frame.value as BrowserState));
  void ctx.bus.request<BrowserState>("viewer.browser:_:state").then(sync).catch(() => undefined);
  return {
    type: "browser",
    icon: "bi-window-desktop",
    title: "Browser",
    instances,
    create: async () => {
      const page = await ctx.bus.request<{ page_id: string }>("viewer.browser:_:create", { url: "about:blank" });
      return page.page_id;
    },
    remove: async (pageID: string) => {
      await ctx.bus.request("viewer.browser:_:close", { page_id: pageID });
    },
  };
}

export default definePlugin({
  id: "viewer.browser",
  name: "Browser",
  components: {
    browser: defineAsyncComponent(() => import("./BrowserPane.vue")),
  },
  settings: defineAsyncComponent(() => import("./BrowserSettings.vue")),
  createDockProvider,
});
