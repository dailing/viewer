import type { BusFrame } from "@viewer/bus-sdk";
import { defineStore } from "pinia";

import type { PluginCtx } from "../../shell/ctx";

export type VoiceJobStatus =
  | "idle"
  | "connecting"
  | "recording"
  | "processing"
  | "ready"
  | "error";

export interface VoiceContextState {
  status: VoiceJobStatus;
  text: string;
  error: string;
  unread: boolean;
  updatedAt: number;
}

interface VoiceMessage {
  type: "ready" | "processing" | "partial" | "committed" | "final" | "error";
  text?: string;
  message?: string;
}

interface VoiceRuntimeJob {
  id: string;
  contextId: string;
  recId: string;
  recorder: MediaRecorder | null;
  stream: MediaStream | null;
  ready: boolean;
  segmentId: string;
  selectedMimeType: string;
  pendingChunkSends: Promise<void>[];
}

interface VoiceSegment {
  id: string;
  text: string;
}

interface VoiceComposition {
  baseText: string;
  segments: VoiceSegment[];
}

let voiceCtx: PluginCtx | null = null;
let voiceJobCounter = 0;
const runtimeJobs = new Map<string, VoiceRuntimeJob>();
const jobsByRecording = new Map<string, VoiceRuntimeJob>();
const compositions = new Map<string, VoiceComposition>();
const earlyEvents = new Map<string, VoiceMessage[]>();
const processedFrames = new WeakSet<object>();
const VOICE_CONTEXTS_KEY = "viewer.voiceContexts.v1";

function loadVoiceContexts(): Record<string, VoiceContextState> {
  try {
    const parsed = JSON.parse(localStorage.getItem(VOICE_CONTEXTS_KEY) ?? "{}") as Record<string, Partial<VoiceContextState>>;
    return Object.fromEntries(Object.entries(parsed).map(([id, value]) => {
      const text = typeof value.text === "string" ? value.text : "";
      const interrupted = ["connecting", "recording", "processing"].includes(String(value.status));
      return [id, {
        status: interrupted ? (text.trim() ? "ready" : "idle") : value.status ?? "idle",
        text,
        error: interrupted ? "页面刷新中断了录音，已保留刷新前的转写内容" : value.error ?? "",
        unread: value.unread === true,
        updatedAt: typeof value.updatedAt === "number" ? value.updatedAt : Date.now(),
      } satisfies VoiceContextState];
    }));
  } catch {
    return {};
  }
}

function persistVoiceContexts(contexts: Record<string, VoiceContextState>): void {
  localStorage.setItem(VOICE_CONTEXTS_KEY, JSON.stringify(contexts));
}

interface PluginRegistryEntry {
  manifest?: { id?: string };
}

export function setVoiceCtx(ctx: PluginCtx): void {
  voiceCtx = ctx;
  const store = useVoiceStore();
  const handlePluginRegistryFrame = (frame: BusFrame): void => {
    store.backendAvailable =
      Array.isArray(frame.value) &&
      frame.value.some(
        (entry: PluginRegistryEntry) => entry?.manifest?.id === "voice",
      );
  };
  store.languageModelRefine = ctx.storage.get("languageModelRefine", true);
  ctx.bus.subscribe("voice:*:event", handleVoiceFrame);
  ctx.bus.subscribe("plugins:_:list", handlePluginRegistryFrame);
  ctx.onDispose(() => {
    ctx.bus.unsubscribe("plugins:_:list", handlePluginRegistryFrame);
    store.backendAvailable = false;
    voiceCtx = null;
    for (const job of [...runtimeJobs.values()]) void cancelRuntime(job);
  });
}

function handleVoiceFrame(frame: BusFrame): void {
  if (processedFrames.has(frame)) return;
  processedFrames.add(frame);
  const parts = frame.channel.split(":");
  const recId = parts.length === 3 ? (parts[1] ?? "") : "";
  if (recId === "" || !isVoiceMessage(frame.value)) return;
  const job = jobsByRecording.get(recId);
  if (job === undefined) {
    console.info("[voice] event buffered (no job yet)", { recId, type: frame.value.type });
    const pending = earlyEvents.get(recId) ?? [];
    pending.push(frame.value);
    earlyEvents.set(recId, pending);
    return;
  }
  useVoiceStore().handleEvent(job, frame.value);
}

function isVoiceMessage(value: unknown): value is VoiceMessage {
  if (typeof value !== "object" || value === null) return false;
  const type = (value as { type?: unknown }).type;
  return ["ready", "processing", "partial", "committed", "final", "error"].includes(
    String(type),
  );
}

function requireCtx(): PluginCtx {
  if (voiceCtx === null) throw new Error("Voice plugin is not active.");
  return voiceCtx;
}

function defaultState(text = ""): VoiceContextState {
  return { status: "idle", text, error: "", unread: false, updatedAt: Date.now() };
}

function supportedVoiceMimeType(): string | undefined {
  return ["audio/webm;codecs=opus", "audio/webm", "audio/mp4"].find((candidate) =>
    MediaRecorder.isTypeSupported(candidate),
  );
}

function appendTranscription(baseText: string, transcript: string): string {
  const cleanTranscript = transcript.trim();
  if (cleanTranscript === "") return baseText;
  const separator = baseText !== "" && !/\s$/.test(baseText) ? " " : "";
  return `${baseText}${separator}${cleanTranscript}`;
}

// Map getUserMedia/MediaRecorder DOMExceptions to a readable reason so the
// composer can show *why* recording failed, not just a bare error icon.
function describeVoiceError(cause: unknown): string {
  const name =
    cause instanceof DOMException
      ? cause.name
      : cause instanceof Error
        ? cause.name
        : "";
  switch (name) {
    case "NotFoundError":
    case "DevicesNotFoundError":
      return "麦克风未找到：设备未连接或被系统禁用";
    case "NotAllowedError":
    case "PermissionDeniedError":
      return "麦克风权限被拒绝，请在浏览器地址栏允许麦克风";
    case "NotReadableError":
    case "TrackStartError":
      return "麦克风被其他程序占用，无法启动";
    case "OverconstrainedError":
      return "麦克风不满足音频采集要求";
    case "SecurityError":
      return "当前页面不是安全上下文（需要 HTTPS 或 localhost）";
    case "AbortError":
      return "麦克风初始化被中断";
    default:
      return cause instanceof Error ? cause.message : String(cause);
  }
}

function contextHasRuntimeJobs(contextId: string): boolean {
  return [...runtimeJobs.values()].some((job) => job.contextId === contextId);
}

function activeRecordingJobForContext(contextId: string): VoiceRuntimeJob | null {
  return (
    [...runtimeJobs.values()].find(
      (job) => job.contextId === contextId && job.recorder !== null,
    ) ?? null
  );
}

/** Live mic stream of a context's in-flight recording, for client-side
 *  silence detection (voice command mode). Null when not recording. */
export function voiceStreamForContext(contextId: string): MediaStream | null {
  return activeRecordingJobForContext(contextId)?.stream ?? null;
}

function composeVoiceText(composition: VoiceComposition): string {
  return composition.segments.reduce(
    (text, segment) => appendTranscription(text, segment.text),
    composition.baseText,
  );
}

function blobToBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onloadend = () => {
      const result = reader.result as string;
      const index = result.indexOf(",");
      resolve(index >= 0 ? result.slice(index + 1) : result);
    };
    reader.onerror = () => reject(reader.error ?? new Error("Failed to read audio chunk as base64"));
    reader.readAsDataURL(blob);
  });
}

async function waitForRecorderStop(recorder: MediaRecorder): Promise<void> {
  if (recorder.state === "inactive") return;
  await new Promise<void>((resolve) => {
    recorder.addEventListener("stop", () => resolve(), { once: true });
    recorder.stop();
  });
}

async function cancelRuntime(job: VoiceRuntimeJob): Promise<void> {
  try {
    if (job.recId !== "" && voiceCtx !== null) {
      await voiceCtx.bus.request("voice:_:cancel", { rec_id: job.recId });
    }
  } catch {
    // Cancellation is best effort; local microphone cleanup must still finish.
  } finally {
    cleanupRuntime(job);
  }
}

function cleanupRuntime(job: VoiceRuntimeJob): void {
  job.stream?.getTracks().forEach((track) => track.stop());
  job.stream = null;
  job.recorder = null;
  runtimeJobs.delete(job.id);
  if (job.recId !== "") {
    voiceCtx?.bus.unsubscribe(`voice:${job.recId}:event`, handleVoiceFrame);
    jobsByRecording.delete(job.recId);
    earlyEvents.delete(job.recId);
  }
  const store = useVoiceStore();
  if (store.activeRecordingJobId === job.id) {
    store.activeRecordingContextId = "";
    store.activeRecordingJobId = "";
  }
}

export const useVoiceStore = defineStore("next-voice", {
  state: () => ({
    contexts: loadVoiceContexts(),
    activeRecordingContextId: "",
    activeRecordingJobId: "",
    languageModelRefine: true,
    backendAvailable: false,
  }),
  getters: {
    context: (state) => (id: string): VoiceContextState =>
      state.contexts[id] ?? defaultState(),
    isBusy: (state) => (id: string): boolean => {
      const status = state.contexts[id]?.status ?? "idle";
      return ["connecting", "recording", "processing"].includes(status);
    },
    hasReadyText: (state) => (id: string): boolean =>
      state.contexts[id]?.status === "ready" &&
      Boolean(state.contexts[id]?.text.trim()),
  },
  actions: {
    ensure(id: string, text = ""): VoiceContextState {
      this.contexts[id] ??= defaultState(text);
      return this.contexts[id];
    },
    setContext(id: string, patch: Partial<VoiceContextState>): void {
      this.contexts[id] = {
        ...this.ensure(id),
        ...patch,
        updatedAt: Date.now(),
      };
      persistVoiceContexts(this.contexts);
    },
    syncText(id: string, text: string): void {
      const current = this.ensure(id, text);
      if (current.text === text) return;
      if (!contextHasRuntimeJobs(id)) compositions.delete(id);
      this.setContext(id, { text });
    },
    clear(id: string): void {
      compositions.delete(id);
      this.setContext(id, defaultState(""));
    },
    dismissError(id: string): void {
      const current = this.ensure(id);
      if (current.status !== "error") return;
      this.setContext(id, { status: "idle", error: "" });
    },
    setLanguageModelRefine(enabled: boolean): void {
      this.languageModelRefine = enabled;
      voiceCtx?.storage.set("languageModelRefine", enabled);
    },
    async start(id: string, baseText: string, options?: { refine?: boolean }): Promise<void> {
      const ctx = requireCtx();
      console.info("[voice] start clicked", { contextId: id, backendAvailable: this.backendAvailable });
      if (this.activeRecordingContextId !== "") {
        throw new Error("Another voice recording is active.");
      }
      if (!navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === "undefined") {
        console.warn("[voice] recording unsupported", {
          secureContext: window.isSecureContext,
          hasMediaDevices: Boolean(navigator.mediaDevices),
          hasMediaRecorder: typeof MediaRecorder !== "undefined",
        });
        this.setContext(id, {
          status: "error",
          error: "此浏览器不支持录音（需要 HTTPS 或 localhost 安全上下文）",
        });
        return;
      }

      const jobId = `${id}:${Date.now()}:${++voiceJobCounter}`;
      const composition = this.prepareComposition(id, baseText);
      composition.segments.push({ id: jobId, text: "" });
      this.setContext(id, {
        status: "connecting",
        text: composeVoiceText(composition),
        error: "",
        unread: false,
      });
      this.activeRecordingContextId = id;
      this.activeRecordingJobId = jobId;

      let job: VoiceRuntimeJob | null = null;
      try {
        const stream = await navigator.mediaDevices.getUserMedia({
          audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true },
        });
        console.info("[voice] getUserMedia ok");
        const selectedMimeType = supportedVoiceMimeType() ?? "";
        const recorder = new MediaRecorder(
          stream,
          selectedMimeType !== "" ? { mimeType: selectedMimeType } : undefined,
        );
        job = {
          id: jobId,
          contextId: id,
          recId: "",
          recorder,
          stream,
          ready: false,
          segmentId: jobId,
          selectedMimeType,
          pendingChunkSends: [],
        };
        runtimeJobs.set(jobId, job);
        recorder.addEventListener("dataavailable", (event) => {
          if (job === null || !job.ready || event.data.size <= 0 || job.recId === "") return;
          const send = blobToBase64(event.data)
            .then((data) =>
              ctx.bus.publish(`voice:${job?.recId ?? ""}:chunk`, {
                data,
              }),
            )
            .then(() => undefined)
            .catch((cause: unknown) => {
              console.warn("[voice] chunk publish failed", cause);
            });
          job.pendingChunkSends.push(send);
          void send.finally(() => {
            if (job !== null) {
              job.pendingChunkSends = job.pendingChunkSends.filter(
                (pending) => pending !== send,
              );
            }
          });
        });
        const result = (await ctx.bus.request("voice:_:start", {
          mime_type: selectedMimeType,
          llm_refine: options?.refine ?? this.languageModelRefine,
        })) as { rec_id: string };
        job.recId = result.rec_id;
        console.info("[voice] start RPC ok", { recId: job.recId, mimeType: selectedMimeType });
        jobsByRecording.set(job.recId, job);
        ctx.bus.subscribe(`voice:${job.recId}:event`, handleVoiceFrame);
        for (const message of earlyEvents.get(job.recId) ?? []) this.handleEvent(job, message);
        earlyEvents.delete(job.recId);
      } catch (cause) {
        console.warn("[voice] start failed", cause);
        this.setContext(id, {
          status: "error",
          error: describeVoiceError(cause),
        });
        if (job !== null) await cancelRuntime(job);
        else {
          this.activeRecordingContextId = "";
          this.activeRecordingJobId = "";
        }
      }
    },
    async stop(id: string, graceful = true): Promise<void> {
      const job = activeRecordingJobForContext(id);
      if (job === null) {
        if (this.activeRecordingContextId === id) {
          this.activeRecordingContextId = "";
          this.activeRecordingJobId = "";
        }
        return;
      }
      if (!graceful) {
        await cancelRuntime(job);
        if (this.contexts[id]?.status !== "error") this.setContext(id, { status: "idle" });
        return;
      }
      if (job.recorder !== null) await waitForRecorderStop(job.recorder);
      await Promise.allSettled(job.pendingChunkSends);
      job.stream?.getTracks().forEach((track) => track.stop());
      job.stream = null;
      job.recorder = null;
      if (this.activeRecordingJobId === job.id) {
        this.activeRecordingContextId = "";
        this.activeRecordingJobId = "";
      }
      this.setContext(id, { status: "processing" });
      try {
        await requireCtx().bus.publish(`voice:${job.recId}:stop`, {});
        console.info("[voice] stop published", { recId: job.recId });
      } catch (cause) {
        console.warn("[voice] stop publish failed", cause);
        this.setContext(id, {
          status: "error",
          error: cause instanceof Error ? cause.message : String(cause),
        });
        await cancelRuntime(job);
      }
    },
    async cancel(id: string): Promise<void> {
      const jobs = [...runtimeJobs.values()].filter((job) => job.contextId === id);
      await Promise.allSettled(jobs.map((job) => cancelRuntime(job)));
      if (this.contexts[id]?.status !== "error") this.setContext(id, { status: "idle" });
    },
    async finishForSend(id: string): Promise<string> {
      // getUserMedia and the start RPC are asynchronous. If send is clicked
      // during that window, wait for recording to become stoppable instead
      // of dispatching the pre-refine partial draft.
      if (this.context(id).status === "connecting") {
        await this.waitForStatus(id, (status) => status !== "connecting", 30_000);
      }
      if (this.context(id).status === "recording") await this.stop(id);
      if (["connecting", "recording", "processing"].includes(this.context(id).status)) {
        await this.waitForStatus(id, (status) => !["connecting", "recording", "processing"].includes(status), 20 * 60_000);
      }
      const state = this.context(id);
      if (state.status === "error") throw new Error(state.error || "Voice input failed.");
      return state.text;
    },
    async waitForStatus(id: string, accept: (status: VoiceJobStatus) => boolean, timeoutMs: number): Promise<void> {
      if (accept(this.context(id).status)) return;
      await new Promise<void>((resolve, reject) => {
        const started = Date.now();
        const timer = window.setInterval(() => {
          if (accept(this.context(id).status)) {
            window.clearInterval(timer);
            resolve();
          } else if (Date.now() - started >= timeoutMs) {
            window.clearInterval(timer);
            reject(new Error("Timed out waiting for voice processing."));
          }
        }, 100);
      });
    },
    handleEvent(job: VoiceRuntimeJob, message: VoiceMessage): void {
      if (runtimeJobs.get(job.id) !== job) return;
      const id = job.contextId;
      if (message.type === "error") {
        console.warn("[voice] service error event", { recId: job.recId, message: message.message });
      } else {
        console.info("[voice] event", { recId: job.recId, type: message.type });
      }
      if (message.type === "ready") {
        job.ready = true;
        if (job.recorder?.state === "inactive") {
          job.recorder.start(250);
          this.setContext(id, { status: "recording", error: "" });
        }
      } else if (message.type === "processing") {
        this.setContext(id, { status: "processing", error: "" });
      } else if (message.type === "partial" || message.type === "committed") {
        this.setContext(id, {
          status: this.activeRecordingContextId === id ? "recording" : "processing",
          text: this.updateSegmentText(job, message.text ?? ""),
          error: "",
          unread: false,
        });
      } else if (message.type === "final") {
        const text = this.updateSegmentText(job, message.text ?? "");
        cleanupRuntime(job);
        const nextStatus =
          this.activeRecordingContextId === id
            ? "recording"
            : contextHasRuntimeJobs(id)
              ? "processing"
              : "ready";
        this.setContext(id, { status: nextStatus, text, error: "", unread: true });
      } else {
        this.setContext(id, {
          status: "error",
          error: message.message ?? "Voice input failed.",
        });
        void cancelRuntime(job);
      }
    },
    prepareComposition(id: string, baseText: string): VoiceComposition {
      if (!contextHasRuntimeJobs(id)) {
        const composition = { baseText, segments: [] };
        compositions.set(id, composition);
        return composition;
      }
      const existing = compositions.get(id);
      if (existing !== undefined) return existing;
      const composition = { baseText, segments: [] };
      compositions.set(id, composition);
      return composition;
    },
    updateSegmentText(job: VoiceRuntimeJob, transcript: string): string {
      const composition =
        compositions.get(job.contextId) ??
        this.prepareComposition(job.contextId, this.contexts[job.contextId]?.text ?? "");
      const segment = composition.segments.find((item) => item.id === job.segmentId);
      if (segment !== undefined) segment.text = transcript;
      return composeVoiceText(composition);
    },
  },
});
