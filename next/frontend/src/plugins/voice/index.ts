import { definePlugin } from "../../shell/definePlugin";
import { setVoiceCtx } from "./voiceStore";

export default definePlugin({
  id: "voice",
  activate(ctx) {
    setVoiceCtx(ctx);
  },
});
