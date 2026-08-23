/**
 * Global voice-control loop (framework: voice actions, slice 3) — hands-free
 * control for the whole shell. Unlike slice 1's per-pane controller this one
 * lives at plugin scope, survives pane switches, and loops:
 *
 *   off ──toggle──▶ command ──final──▶ RPC voice-control:_:command ──▶ speak + effects ──┐
 *                      ▲                                                                 │
 *                      │            effect start_dictation ──▶ voice-fx mailbox ──▶ pane dictates
 *                      │                                                                │
 *                      └────────────────── "voice-control:_:resume" (dictation done) ◀───┘
 *
 * Endpointing is silence-based (watchTrailingSilence), never in-band
 * keywords. The only local keyword match is the exit phrase ("退出语音"),
 * matched against the whole short command utterance — no ambiguity with
 * dictated content, because dictation runs inside the target pane.
 *
 * Proactive announcements (slice 4): `voice-control:_:announce` frames are
 * spoken while the loop is on; mid-listen the mic is paused so TTS doesn't
 * echo into it, then listening resumes.
 */
import { nextTick, ref, watch, type Ref } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { useLayoutStore } from "../../stores/layout";
import { watchTrailingSilence, type SilenceWatchHandle } from "../voice/silence";
import { speakText, stopSpeaking } from "../voice/speak";
import { useVoiceStore, voiceStreamForContext } from "../voice/voiceStore";

export type VoiceControlPhase = "off" | "command" | "busy" | "awaiting";

/** Module-scope singleton: the loader's createDockActions/activate and the
 *  debug pane share whichever instance exists. */
let activeController: VoiceControlController | null = null;

export function ensureController(ctx: PluginCtx): VoiceControlController {
  activeController ??= new VoiceControlController(ctx);
  return activeController;
}

export function getController(): VoiceControlController | null {
  return activeController;
}

export function disposeController(): void {
  activeController?.dispose();
  activeController = null;
}

interface VoiceEffect {
  type: string;
  plugin?: string;
  pane_type?: string;
  instance_id?: string;
}

interface VoiceCommandResult {
  ok: boolean;
  say: string;
  entry_id?: string;
  transcript: string;
  effects: VoiceEffect[];
}

const COMMAND_CONTEXT_ID = "voice-control:command";
/** Trailing silence that ends a short command utterance. */
const COMMAND_SILENCE_MS = 2000;
const EXIT_PHRASES = ["退出语音", "关闭语音", "退出语音模式", "关闭语音模式", "stoplistening", "exitvoice"];

export class VoiceControlController {
  readonly phase: Ref<VoiceControlPhase> = ref("off");
  private readonly voice = useVoiceStore();
  private readonly layout = useLayoutStore();
  private silenceWatch: SilenceWatchHandle | null = null;
  private disposed = false;
  /** Monotonic nonce for voice-fx handoffs. */
  private fxNonce = 0;

  constructor(private readonly ctx: PluginCtx) {
    watch(
      () => this.voice.context(COMMAND_CONTEXT_ID).status,
      (status) => {
        if (this.phase.value !== "command") return;
        if (status === "recording") this.armSilence();
        if (status === "ready") void this.onCommandFinal();
        if (status === "error") this.failAloud(this.voice.context(COMMAND_CONTEXT_ID).error);
      },
    );
    ctx.bus.subscribe("voice-control:_:resume", (frame) => {
      const value = frame.value as { type?: string } | null;
      if (value?.type !== "dictation_done") return;
      if (this.phase.value !== "awaiting") return;
      this.listen();
    });
    ctx.bus.subscribe("voice-control:_:announce", (frame) => {
      const value = frame.value as { say?: string } | null;
      if (value === null || typeof value.say !== "string" || value.say === "") return;
      if (this.phase.value === "off") return; // backend gates too; belt and suspenders
      this.announce(value.say);
    });
    window.addEventListener("keydown", this.handleHotkey);
  }

  dispose(): void {
    this.disposed = true;
    window.removeEventListener("keydown", this.handleHotkey);
    void this.cancel();
  }

  /** Ctrl+Shift+M toggles the loop globally. */
  private readonly handleHotkey = (event: KeyboardEvent): void => {
    if (!event.ctrlKey || !event.shiftKey || event.code !== "KeyM") return;
    event.preventDefault();
    void this.toggle();
  };

  async toggle(): Promise<void> {
    if (this.phase.value !== "off") {
      await this.cancel();
      speakText("已退出语音控制");
      return;
    }
    if (!this.voice.backendAvailable) {
      speakText("语音不可用，voice 插件未运行");
      return;
    }
    stopSpeaking();
    this.setBackendEnabled(true);
    // Enter the loop BEFORE speaking: listen() refuses to run while the phase
    // is still "off", and it only fires after the announcement ends.
    this.phase.value = "command";
    speakText("语音控制已开启", () => this.listen());
  }

  /** One listen → execute → speak → listen again cycle. */
  private listen(): void {
    if (this.disposed || this.phase.value === "off") return;
    this.phase.value = "command";
    // Commands skip LLM refine: raw ASR is faster and keeps keywords intact.
    this.voice.start(COMMAND_CONTEXT_ID, "", { refine: false }).catch((cause: unknown) => {
      this.phase.value = "off";
      this.setBackendEnabled(false);
      speakText(cause instanceof Error && cause.message.includes("active") ? "另一个录音正在进行" : "录音启动失败");
    });
  }

  private armSilence(): void {
    if (this.silenceWatch !== null) return;
    const stream = voiceStreamForContext(COMMAND_CONTEXT_ID);
    if (stream === null) return;
    this.silenceWatch = watchTrailingSilence(stream, {
      silenceMs: COMMAND_SILENCE_MS,
      onSilence: () => {
        this.silenceWatch?.stop();
        this.silenceWatch = null;
        void this.voice.stop(COMMAND_CONTEXT_ID);
      },
    });
  }

  private releaseSilence(): void {
    this.silenceWatch?.stop();
    this.silenceWatch = null;
  }

  private async onCommandFinal(): Promise<void> {
    if (this.phase.value !== "command") return;
    const text = this.voice.context(COMMAND_CONTEXT_ID).text.trim();
    this.voice.clear(COMMAND_CONTEXT_ID);
    if (text === "") {
      // Silence-only utterance: keep looping without a spoken scold.
      this.listen();
      return;
    }
    const normalized = text.toLowerCase().replace(/[\s。！？，,.!?]/g, "");
    if (EXIT_PHRASES.includes(normalized)) {
      await this.cancel();
      speakText("已退出语音控制");
      return;
    }
    await this.executeCommand(text);
  }

  private async executeCommand(text: string): Promise<void> {
    this.phase.value = "busy";
    let result: VoiceCommandResult;
    try {
      result = (await this.ctx.bus.request("voice-control:_:command", {
        text,
        phase: this.phase.value,
      })) as VoiceCommandResult;
    } catch {
      speakText("命令执行失败", () => this.listen());
      return;
    }
    const dictate = result.effects.find((effect) => effect.type !== "open_instance");
    for (const effect of result.effects) {
      if (effect.type === "open_instance" && typeof effect.instance_id === "string") {
        this.layout.openInstance(effect.pane_type ?? "chat", effect.instance_id);
      }
    }
    if (dictate !== undefined && result.ok) {
      // Hand off to the target pane AFTER the spoken prompt ends so our own
      // TTS doesn't leak into the dictation mic. The pane must be open (and
      // subscribed) first: openInstance + nextTick, then the retained
      // voice-fx mailbox redelivers even if the subscription landed late.
      this.phase.value = "awaiting";
      speakText(result.say, () => void this.handoffDictation(dictate));
      return;
    }
    speakText(result.say, () => this.listen());
  }

  private async handoffDictation(effect: VoiceEffect): Promise<void> {
    if (this.disposed || this.phase.value !== "awaiting") return;
    const paneType = effect.pane_type ?? "chat";
    const instanceId = effect.instance_id ?? "";
    if (instanceId === "") {
      this.listen();
      return;
    }
    this.layout.openInstance(paneType, instanceId);
    await nextTick();
    this.fxNonce += 1;
    this.ctx.bus.set(`voice-fx:${effect.plugin ?? paneType}:${instanceId}`, {
      type: effect.type,
      nonce: `${Date.now()}-${this.fxNonce}`,
    });
    // Safety net: if the pane never answers (e.g. it failed to mount), the
    // loop would wait forever; resume listening after a bounded pause.
    window.setTimeout(() => {
      if (this.phase.value === "awaiting") this.listen();
    }, 60_000);
  }

  /** Pause listening, speak an announcement, resume. */
  private announce(say: string): void {
    const wasListening = this.phase.value === "command";
    if (wasListening) {
      this.releaseSilence();
      const status = this.voice.context(COMMAND_CONTEXT_ID).status;
      if (["connecting", "recording", "processing"].includes(status)) {
        void this.voice.cancel(COMMAND_CONTEXT_ID);
      }
    }
    speakText(say, () => {
      if (wasListening && this.phase.value === "command") this.listen();
    });
  }

  private failAloud(message: string): void {
    this.releaseSilence();
    speakText(message !== "" ? `语音出错：${message}` : "语音出错", () => this.listen());
  }

  private setBackendEnabled(enabled: boolean): void {
    this.ctx.bus.request("voice-control:_:enable", { enabled }).catch(() => undefined);
  }

  async cancel(): Promise<void> {
    this.phase.value = "off";
    this.releaseSilence();
    stopSpeaking();
    this.setBackendEnabled(false);
    const status = this.voice.context(COMMAND_CONTEXT_ID).status;
    if (["connecting", "recording", "processing"].includes(status)) {
      await this.voice.cancel(COMMAND_CONTEXT_ID);
    }
  }
}

/** The Dock-foot toggle action for the global loop. */
export function voiceControlDockAction(controller: VoiceControlController) {
  return {
    id: "voice-control-toggle",
    icon: () => (controller.phase.value === "off" ? "bi-headphones" : "bi-ear-fill"),
    title: () => {
      switch (controller.phase.value) {
        case "command":
          return "正在听命令……说「退出语音」或点击停止（Ctrl+Shift+M）";
        case "busy":
          return "语音命令处理中……（点击停止）";
        case "awaiting":
          return "等待口述完成……（点击停止）";
        default:
          return "语音控制（Ctrl+Shift+M）：连续语音对话，可提问或操作——打开聊天、读回复、口述发消息、问状态、停止运行";
      }
    },
    active: () => controller.phase.value !== "off",
    onClick: () => void controller.toggle(),
  };
}
