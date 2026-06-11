import { defineStore } from "pinia";
import { voiceSocketUrl } from "../api/client";

export type VoiceJobStatus = "idle" | "connecting" | "recording" | "processing" | "ready" | "error";

export type VoiceContextState = {
  status: VoiceJobStatus;
  text: string;
  error: string;
  unread: boolean;
  updatedAt: number;
};

type VoiceMessage = { type: "ready" | "processing" | "partial" | "committed" | "final" | "error"; text?: string; message?: string };
type VoiceRuntimeJob = {
  id: string;
  contextId: string;
  socket: WebSocket;
  recorder: MediaRecorder | null;
  stream: MediaStream | null;
  ready: boolean;
  segmentId: string;
  selectedMimeType: string;
  pendingChunkSends: Promise<void>[];
};
type VoiceSegment = {
  id: string;
  text: string;
};
type VoiceComposition = {
  baseText: string;
  segments: VoiceSegment[];
};

const runtimeJobs = new Map<string, VoiceRuntimeJob>();
const compositions = new Map<string, VoiceComposition>();
let voiceJobCounter = 0;
const LANGUAGE_MODEL_REFINE_STORAGE_KEY = "viewer.voice.languageModelRefine";

function defaultState(text = ""): VoiceContextState {
  return { status: "idle", text, error: "", unread: false, updatedAt: Date.now() };
}

function supportedVoiceMimeType(): string | undefined {
  const candidates = ["audio/webm;codecs=opus", "audio/webm", "audio/mp4"];
  return candidates.find((candidate) => MediaRecorder.isTypeSupported(candidate));
}

function appendTranscription(baseText: string, transcript: string): string {
  const cleanTranscript = transcript.trim();
  if (!cleanTranscript) return baseText;
  const separator = baseText && !/\s$/.test(baseText) ? " " : "";
  return `${baseText}${separator}${cleanTranscript}`;
}

function contextHasRuntimeJobs(contextId: string): boolean {
  for (const job of runtimeJobs.values()) {
    if (job.contextId === contextId) return true;
  }
  return false;
}

function activeRecordingJobForContext(contextId: string): VoiceRuntimeJob | null {
  for (const job of runtimeJobs.values()) {
    if (job.contextId === contextId && job.recorder) return job;
  }
  return null;
}

function composeVoiceText(composition: VoiceComposition): string {
  return composition.segments.reduce((text, segment) => appendTranscription(text, segment.text), composition.baseText);
}

async function waitForSocketClose(socket: WebSocket, timeoutMs = 12000): Promise<void> {
  if (socket.readyState === WebSocket.CLOSED) return;
  await new Promise<void>((resolve) => {
    const timeout = window.setTimeout(resolve, timeoutMs);
    socket.addEventListener(
      "close",
      () => {
        window.clearTimeout(timeout);
        resolve();
      },
      { once: true },
    );
  });
}

async function waitForRecorderStop(recorder: MediaRecorder): Promise<void> {
  if (recorder.state === "inactive") return;
  await new Promise<void>((resolve) => {
    recorder.addEventListener("stop", () => resolve(), { once: true });
    recorder.stop();
  });
}

export const useVoiceStore = defineStore("voice", {
  state: () => ({
    contexts: {} as Record<string, VoiceContextState>,
    activeRecordingContextId: "",
    activeRecordingJobId: "",
    languageModelRefine: loadLanguageModelRefine(),
  }),
  getters: {
    context: (state) => (id: string): VoiceContextState => state.contexts[id] ?? defaultState(),
    isBusy: (state) => (id: string): boolean => {
      const status = state.contexts[id]?.status ?? "idle";
      return status === "connecting" || status === "recording" || status === "processing";
    },
    hasReadyText: (state) => (id: string): boolean => state.contexts[id]?.status === "ready" && Boolean(state.contexts[id]?.text.trim()),
  },
  actions: {
    ensure(id: string, text = "") {
      if (!this.contexts[id]) {
        this.contexts = { ...this.contexts, [id]: defaultState(text) };
      }
      return this.contexts[id];
    },
    setContext(id: string, patch: Partial<VoiceContextState>) {
      const current = this.ensure(id);
      this.contexts = {
        ...this.contexts,
        [id]: { ...current, ...patch, updatedAt: Date.now() },
      };
    },
    syncText(id: string, text: string) {
      const current = this.ensure(id, text);
      if (current.text === text) return;
      if (!contextHasRuntimeJobs(id)) compositions.delete(id);
      this.setContext(id, { text });
    },
    markRead(id: string) {
      if (!this.contexts[id]?.unread) return;
      this.setContext(id, { unread: false });
    },
    clear(id: string) {
      compositions.delete(id);
      this.setContext(id, defaultState(""));
    },
    setLanguageModelRefine(enabled: boolean) {
      this.languageModelRefine = enabled;
      try {
        window.localStorage.setItem(LANGUAGE_MODEL_REFINE_STORAGE_KEY, enabled ? "1" : "0");
      } catch {
        // Ignore storage failures; the in-memory toggle still works for this page.
      }
    },
    toggleLanguageModelRefine() {
      this.setLanguageModelRefine(!this.languageModelRefine);
    },
    async start(id: string, baseText: string) {
      if (this.activeRecordingContextId) {
        throw new Error("Another voice recording is active.");
      }
      if (!navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === "undefined") {
        this.setContext(id, { status: "error", error: "Voice recording is not supported in this browser." });
        return;
      }

      const jobId = `${id}:${Date.now()}:${++voiceJobCounter}`;
      const composition = this.prepareComposition(id, baseText);
      composition.segments.push({ id: jobId, text: "" });
      this.setContext(id, { status: "connecting", text: composeVoiceText(composition), error: "", unread: false });
      this.activeRecordingContextId = id;
      this.activeRecordingJobId = jobId;

      try {
        const stream = await navigator.mediaDevices.getUserMedia({
          audio: {
            channelCount: 1,
            echoCancellation: true,
            noiseSuppression: true,
          },
        });
        const selectedMimeType = supportedVoiceMimeType() ?? "";
        const recorder = new MediaRecorder(stream, selectedMimeType ? { mimeType: selectedMimeType } : undefined);
        const socket = new WebSocket(voiceSocketUrl());
        socket.binaryType = "arraybuffer";
        const job: VoiceRuntimeJob = {
          id: jobId,
          contextId: id,
          socket,
          recorder,
          stream,
          ready: false,
          segmentId: jobId,
          selectedMimeType,
          pendingChunkSends: [],
        };
        runtimeJobs.set(jobId, job);

        const sendVoiceChunk = async (blob: Blob) => {
          if (!job.ready || blob.size <= 0 || socket.readyState !== WebSocket.OPEN) return;
          const data = await blob.arrayBuffer();
          if (socket.readyState === WebSocket.OPEN) socket.send(data);
        };

        socket.addEventListener("message", (event) => {
          const message = JSON.parse(event.data) as VoiceMessage;
          if (message.type === "ready") {
            job.ready = true;
            if (socket.readyState === WebSocket.OPEN) {
              socket.send(
                JSON.stringify({
                  type: "start",
                  mimeType: selectedMimeType,
                  llm_refine: this.languageModelRefine,
                  language_model_refine: this.languageModelRefine,
                  languageModelRefine: this.languageModelRefine,
                }),
              );
            }
            if (job.recorder?.state === "inactive") {
              job.recorder.start(250);
              this.setContext(id, { status: "recording", error: "" });
            }
          } else if (message.type === "processing") {
            this.setContext(id, { status: "processing", error: "" });
          } else if (message.type === "partial" || message.type === "committed") {
            const nextStatus = this.activeRecordingContextId === id ? "recording" : "processing";
            this.setContext(id, { status: nextStatus, text: this.updateSegmentText(job, message.text ?? ""), error: "", unread: false });
          } else if (message.type === "final") {
            const text = this.updateSegmentText(job, message.text ?? "");
            this.cleanupRuntime(job.id, job);
            const nextStatus = this.activeRecordingContextId === id ? "recording" : contextHasRuntimeJobs(id) ? "processing" : "ready";
            this.setContext(id, { status: nextStatus, text, error: "", unread: true });
          } else if (message.type === "error") {
            this.setContext(id, { status: "error", error: message.message ?? "Voice input failed." });
            void this.stopJob(job, false);
          }
        });
        socket.addEventListener("close", () => {
          if (runtimeJobs.get(job.id) === job && this.contexts[id]?.status !== "ready" && this.contexts[id]?.status !== "error") {
            this.cleanupRuntime(job.id, job);
            this.setContext(id, { status: contextHasRuntimeJobs(id) ? "processing" : this.contexts[id]?.text.trim() ? "ready" : "idle" });
          } else {
            this.cleanupRuntime(job.id, job);
          }
        });
        socket.addEventListener("error", () => {
          this.setContext(id, { status: "error", error: "Voice input connection failed." });
          void this.stopJob(job, false);
        });

        recorder.addEventListener("dataavailable", (event) => {
          const send = sendVoiceChunk(event.data);
          job.pendingChunkSends.push(send);
          void send.finally(() => {
            job.pendingChunkSends = job.pendingChunkSends.filter((pending) => pending !== send);
          });
        });
      } catch (err) {
        this.setContext(id, { status: "error", error: err instanceof Error ? err.message : String(err) });
        await this.stop(id, false);
      }
    },
    async stop(id: string, graceful = true) {
      const job = activeRecordingJobForContext(id);
      if (!job) {
        if (this.activeRecordingContextId === id) {
          this.activeRecordingContextId = "";
          this.activeRecordingJobId = "";
        }
        return;
      }
      await this.stopJob(job, graceful);
    },
    async stopJob(job: VoiceRuntimeJob, graceful = true) {
      const id = job.contextId;
      const socket = job.socket;
      try {
        if (graceful && job.recorder) {
          await waitForRecorderStop(job.recorder);
          await Promise.allSettled(job.pendingChunkSends);
        } else if (job.recorder && job.recorder.state !== "inactive") {
          job.recorder.stop();
        }
        job.stream?.getTracks().forEach((track) => track.stop());
        job.stream = null;
        job.recorder = null;
        if (this.activeRecordingJobId === job.id) {
          this.activeRecordingContextId = "";
          this.activeRecordingJobId = "";
        }
        if (graceful && socket.readyState === WebSocket.OPEN) {
          this.setContext(id, { status: "processing" });
          socket.send(JSON.stringify({ type: "stop" }));
          await waitForSocketClose(socket, 120000);
        } else {
          socket.close();
        }
      } finally {
        if (socket.readyState === WebSocket.OPEN) socket.close();
        if (!graceful && this.contexts[id]?.status !== "error") {
          this.setContext(id, { status: "idle" });
        }
        this.cleanupRuntime(job.id, job);
      }
    },
    prepareComposition(id: string, baseText: string): VoiceComposition {
      if (!contextHasRuntimeJobs(id)) {
        const composition = { baseText, segments: [] };
        compositions.set(id, composition);
        return composition;
      }
      const existing = compositions.get(id);
      if (existing) return existing;
      const composition = { baseText, segments: [] };
      compositions.set(id, composition);
      return composition;
    },
    updateSegmentText(job: VoiceRuntimeJob, transcript: string): string {
      const composition = compositions.get(job.contextId) ?? this.prepareComposition(job.contextId, this.contexts[job.contextId]?.text ?? "");
      const segment = composition.segments.find((item) => item.id === job.segmentId);
      if (segment) segment.text = transcript;
      return composeVoiceText(composition);
    },
    cleanupRuntime(jobId: string, expectedJob?: VoiceRuntimeJob) {
      const job = runtimeJobs.get(jobId);
      if (expectedJob && job && job !== expectedJob) return;
      job?.stream?.getTracks().forEach((track) => track.stop());
      if (!expectedJob || job === expectedJob) runtimeJobs.delete(jobId);
      if (job && this.activeRecordingJobId === job.id && (!expectedJob || job === expectedJob)) {
        this.activeRecordingContextId = "";
        this.activeRecordingJobId = "";
      }
    },
  },
});

function loadLanguageModelRefine(): boolean {
  try {
    const stored = window.localStorage.getItem(LANGUAGE_MODEL_REFINE_STORAGE_KEY);
    if (stored === "0") return false;
    if (stored === "1") return true;
  } catch {
    // Ignore storage failures and use the requested default.
  }
  return true;
}
