import { definePlugin } from "../../shell/definePlugin";
import { setVoiceCtx } from "./voiceStore";

export default definePlugin({
  id: "voice",
  name: "语音输入",
  activate(ctx) {
    setVoiceCtx(ctx);
  },
});
