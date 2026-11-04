/**
 * voice-control frontend (framework: voice actions, slice 3): the global
 * hands-free loop plus plugin-owned settings (interaction log, prompt
 * templates, sniffed capability catalog). The Dock-foot headphone remains a
 * one-click operational toggle; configuration lives in 插件管理.
 */
import { defineAsyncComponent } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { DockAction } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import {
  disposeController,
  ensureController,
  voiceControlDockAction,
} from "./controller";

export default definePlugin({
  id: "voice-control",
  name: "语音控制",
  settings: defineAsyncComponent(() => import("./VoiceControlPane.vue")),
  createDockActions: (ctx: PluginCtx): DockAction[] => [voiceControlDockAction(ensureController(ctx))],
  activate: (ctx: PluginCtx) => {
    ensureController(ctx);
  },
  deactivate: () => {
    disposeController();
  },
});
