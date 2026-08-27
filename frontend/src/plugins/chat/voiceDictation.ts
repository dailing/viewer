/**
 * Voice dictation for one chat pane (framework: voice actions, slice 3) —
 * the pane-local half of the global voice-control loop. The global
 * controller (voice-control plugin) resolves commands; when a command
 * resolves to dictation it hands off through the retained
 * `voice-fx:chat:<chatId>` mailbox, and this controller runs:
 *
 *   dictate ──final──▶ confirm ──"发送"──▶ send ──┐
 *                     │  "取消"──▶ clear draft    ├─▶ publish voice-control:_:resume
 *                     └  未匹配 ×3 ─▶ keep draft ──┘   (dictation_done; the loop resumes)
 *
 * Endpointing is silence-based (watchTrailingSilence), never in-band
 * keywords — a keyword can't be told apart from dictated content. Keywords
 * only appear in the confirm phase, where the whole utterance IS the answer.
 *
 * The mailbox is retained, so a stale value would re-fire after a pane
 * remount or page reload; handoffs carry a nonce timestamp and frames older
 * than FX_MAX_AGE_MS or already handled (persisted nonce) are ignored.
 */
import { ref, watch, type Ref } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import { watchTrailingSilence, type SilenceWatchHandle } from "../voice/silence";
import { speakText, stopSpeaking } from "../voice/speak";
import { useVoiceStore, voiceStreamForContext } from "../voice/voiceStore";

export type VoiceDictationPhase = "off" | "dictate" | "confirm";

export interface VoiceDictationHooks {
  /** Composer context id (dictation recording binds to it). */
  contextId: string;
  getDraft: () => string;
  send: () => void;
  clearDraft: () => void;
}

const CONFIRM_CONTEXT_PREFIX = "voice-confirm:";
/** Trailing silence that ends dictation (people pause mid-sentence). */
const DICTATE_SILENCE_MS = 4000;
/** Trailing silence that ends a short confirm utterance. */
const CONFIRM_SILENCE_MS = 2000;
const CONFIRM_MAX_ATTEMPTS = 3;
/** voice-fx handoffs older than this are stale mailbox replays. */
const FX_MAX_AGE_MS = 30_000;
const FX_NONCE_STORAGE_KEY = "voice-fx-nonce";

export class VoiceDictationController {
  readonly phase: Ref<VoiceDictationPhase> = ref("off");
  private readonly voice = useVoiceStore();
  private readonly confirmContextId = CONFIRM_CONTEXT_PREFIX + this.chatId;
  private silenceWatch: SilenceWatchHandle | null = null;
  private confirmAttempts = 0;
  private readonly stopWatches: Array<() => void> = [];
  private disposed = false;
  /** This dictation session was handed off from the global loop. */
  private fromLoop = false;

  constructor(
    private readonly ctx: PluginCtx,
    private readonly chatId: string,
    private readonly hooks: VoiceDictationHooks,
  ) {
    this.stopWatches.push(
      watch(
        () => this.voice.context(this.confirmContextId).status,
        (status) => {
          if (this.phase.value !== "confirm") return;
          if (status === "recording") this.armSilence(this.confirmContextId, CONFIRM_SILENCE_MS);
          if (status === "ready") this.onConfirmFinal();
          if (status === "error") this.failAloud(this.voice.context(this.confirmContextId).error);
        },
      ),
      watch(
        () => this.voice.context(this.hooks.contextId).status,
        (status) => {
          if (this.phase.value !== "dictate") return;
          if (status === "recording") this.armSilence(this.hooks.contextId, DICTATE_SILENCE_MS);
          if (status === "ready") this.onDictateFinal();
          if (status === "error") this.failAloud(this.voice.context(this.hooks.contextId).error);
        },
      ),
    );
    // Handoffs from the global loop arrive on a retained mailbox so a pane
    // mounted moments ago still receives them.
    ctx.bus.subscribe(`voice-fx:chat:${this.chatId}`, (frame) => {
      const value = frame.value as { type?: string; nonce?: string } | null;
      if (value?.type !== "start_dictation" || typeof value.nonce !== "string") return;
      const timestamp = Number(value.nonce.split("-")[0]);
      if (!Number.isFinite(timestamp) || Date.now() - timestamp > FX_MAX_AGE_MS) return;
      if (this.ctx.storage.get(FX_NONCE_STORAGE_KEY, "") === value.nonce) return;
      this.ctx.storage.set(FX_NONCE_STORAGE_KEY, value.nonce);
      void this.beginDictation(true);
    });
  }

  dispose(): void {
    this.disposed = true;
    for (const stop of this.stopWatches) stop();
    this.releaseSilence();
    stopSpeaking();
    // The draft recording belongs to the global input session, not this
    // controller component. Closing/unpinning the pane must leave it running;
    // WorkspaceBar remains able to stop/refine/send it. A short-lived confirm
    // recording has no useful target once this controller is gone, so only
    // that private context is cancelled.
    if (this.phase.value === "confirm") void this.voice.cancel(this.confirmContextId);
    this.phase.value = "off";
    this.fromLoop = false;
  }

  /** Start dictating into this pane's composer. */
  async beginDictation(fromLoop: boolean): Promise<void> {
    if (this.disposed || this.phase.value !== "off") return;
    stopSpeaking();
    this.fromLoop = fromLoop;
    this.phase.value = "dictate";
    this.releaseSilence();
    try {
      await this.voice.start(this.hooks.contextId, this.hooks.getDraft());
    } catch {
      this.finish("录音启动失败");
    }
  }

  private armSilence(contextId: string, silenceMs: number): void {
    if (this.silenceWatch !== null) return;
    const stream = voiceStreamForContext(contextId);
    if (stream === null) return;
    this.silenceWatch = watchTrailingSilence(stream, {
      silenceMs,
      onSilence: () => {
        this.silenceWatch?.stop();
        this.silenceWatch = null;
        void this.voice.stop(contextId);
      },
    });
  }

  private releaseSilence(): void {
    this.silenceWatch?.stop();
    this.silenceWatch = null;
  }

  private onDictateFinal(): void {
    this.phase.value = "confirm";
    this.confirmAttempts = 0;
    speakText("草稿准备好了。说发送确认，说取消作废。", () => void this.listenConfirm());
  }

  private async listenConfirm(): Promise<void> {
    if (this.disposed || this.phase.value !== "confirm") return;
    try {
      // Confirms skip LLM refine: the whole utterance is the keyword.
      await this.voice.start(this.confirmContextId, "", { refine: false });
    } catch {
      this.finish("录音启动失败，草稿已保留");
    }
  }

  private onConfirmFinal(): void {
    if (this.phase.value !== "confirm") return;
    const text = this.voice.context(this.confirmContextId).text.trim();
    this.voice.clear(this.confirmContextId);
    const normalized = text.toLowerCase().replace(/[\s。！？，,.!?]/g, "");
    if (normalized.includes("发送") || normalized.includes("确认") || normalized.includes("send")) {
      this.hooks.send();
      this.finish("已发送");
      return;
    }
    if (normalized.includes("取消") || normalized.includes("算了") || normalized.includes("作废") || normalized.includes("cancel")) {
      this.hooks.clearDraft();
      this.finish("已取消");
      return;
    }
    this.confirmAttempts += 1;
    if (this.confirmAttempts >= CONFIRM_MAX_ATTEMPTS) {
      this.finish("多次没听清，已保留草稿，退出语音输入");
      return;
    }
    speakText("没听清，请说发送或取消。", () => void this.listenConfirm());
  }

  /** End the session: speak the closing line, then resume the global loop
   *  (only when it handed this dictation off). Speaking first keeps the loop
   *  from reopening the mic under our own TTS. */
  private finish(say: string): void {
    this.phase.value = "off";
    this.releaseSilence();
    const resume = this.fromLoop;
    this.fromLoop = false;
    speakText(say, () => {
      if (resume) this.ctx.bus.publish("voice-control:_:resume", { type: "dictation_done" });
    });
  }

  private failAloud(message: string): void {
    this.finish(message !== "" ? `语音出错：${message}` : "语音出错");
  }

  async cancel(): Promise<void> {
    const phase = this.phase.value;
    this.phase.value = "off";
    this.fromLoop = false;
    this.releaseSilence();
    stopSpeaking();
    const confirmStatus = this.voice.context(this.confirmContextId).status;
    if (["connecting", "recording", "processing"].includes(confirmStatus)) {
      await this.voice.cancel(this.confirmContextId);
    }
    if (phase === "dictate") {
      // Graceful stop: already-spoken text still lands in the draft.
      const draftStatus = this.voice.context(this.hooks.contextId).status;
      if (["connecting", "recording"].includes(draftStatus)) {
        await this.voice.stop(this.hooks.contextId);
      }
    }
  }
}
