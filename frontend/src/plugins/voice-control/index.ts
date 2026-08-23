/**
 * voice-control frontend (framework: voice actions, slice 3): the global
 * hands-free loop plus a singleton debug pane (interaction log, prompt
 * templates, sniffed capability catalog). The loader calls
 * createDockActions before activate, so the controller is built lazily from
 * whichever runs first.
 */
import { defineAsyncComponent } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { DockAction, DockProvider } from "../../shell/definePlugin";
import { definePlugin } from "../../shell/definePlugin";
import {
  disposeController,
  ensureController,
  voiceControlDockAction,
} from "./controller";

export default definePlugin({
  id: "voice-control",
  components: {
    "voice-control": defineAsyncComponent(() => import("./VoiceControlPane.vue")),
  },
  createDockProvider: (): DockProvider => ({
    type: "voice-control",
    icon: "bi-mic",
    title: "语音控制",
    singleton: true,
    instances: [],
  }),
  createDockActions: (ctx: PluginCtx): DockAction[] => [voiceControlDockAction(ensureController(ctx))],
  activate: (ctx: PluginCtx) => {
    ensureController(ctx);
  },
  deactivate: () => {
    disposeController();
  },
});
