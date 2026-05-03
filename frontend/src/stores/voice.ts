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

type VoiceMessage = { type: "ready" | "processing" | "final" | "error"; text?: string; message?: string };
type VoiceRuntimeJob = {
  socket: WebSocket;
  recorder: MediaRecorder | null;
  stream: MediaStream | null;
  ready: boolean;
  selectedMimeType: string;
  pendingChunkSends: Promise<void>[];
};

const runtimeJobs = new Map<string, VoiceRuntimeJob>();

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
      this.setContext(id, { text });
    },
    markRead(id: string) {
      if (!this.contexts[id]?.unread) return;
      this.setContext(id, { unread: false });
    },
    clear(id: string) {
      this.setContext(id, defaultState(""));
    },
    async start(id: string, baseText: string) {
      if (this.activeRecordingContextId && this.activeRecordingContextId !== id) {
        throw new Error("Another voice recording is active.");
      }
      if (runtimeJobs.has(id)) return;
      if (!navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === "undefined") {
        this.setContext(id, { status: "error", error: "Voice recording is not supported in this browser." });
        return;
      }

      this.setContext(id, { status: "connecting", text: baseText, error: "", unread: false });
      this.activeRecordingContextId = id;

      try {
        const stream = await navigator.mediaDevices.getUserMedia({
          audio: {
            channelCount: 1,
            echoCancellation: true,
            noiseSuppression: true,
          },
        });
        const selectedMimeType = supportedVoiceMimeType() ?? "";
        const socket = new WebSocket(voiceSocketUrl());
        socket.binaryType = "arraybuffer";
        const job: VoiceRuntimeJob = {
          socket,
          recorder: null,
          stream,
          ready: false,
          selectedMimeType,
          pendingChunkSends: [],
        };
        runtimeJobs.set(id, job);

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
              socket.send(JSON.stringify({ type: "start", mimeType: selectedMimeType }));
            }
            if (job.recorder?.state === "inactive") {
              job.recorder.start(250);
              this.setContext(id, { status: "recording", error: "" });
            }
          } else if (message.type === "processing") {
            this.setContext(id, { status: "processing", error: "" });
          } else if (message.type === "final") {
            const currentText = this.contexts[id]?.text ?? "";
            const nextStatus = this.activeRecordingContextId === id ? "recording" : "ready";
            this.setContext(id, { status: nextStatus, text: appendTranscription(currentText, message.text ?? ""), error: "", unread: true });
            this.cleanupRuntime(id, job);
          } else if (message.type === "error") {
            this.setContext(id, { status: "error", error: message.message ?? "Voice input failed." });
            void this.stop(id, false);
          }
        });
        socket.addEventListener("close", () => {
          if (runtimeJobs.get(id) === job && this.contexts[id]?.status !== "ready" && this.contexts[id]?.status !== "error") {
            this.setContext(id, { status: "idle" });
          }
          this.cleanupRuntime(id, job);
        });
        socket.addEventListener("error", () => {
          this.setContext(id, { status: "error", error: "Voice input connection failed." });
          void this.stop(id, false);
        });

        job.recorder = new MediaRecorder(stream, selectedMimeType ? { mimeType: selectedMimeType } : undefined);
        job.recorder.addEventListener("dataavailable", (event) => {
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
      const job = runtimeJobs.get(id);
      if (!job) {
        if (this.activeRecordingContextId === id) this.activeRecordingContextId = "";
        return;
      }
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
        if (this.activeRecordingContextId === id) this.activeRecordingContextId = "";
        if (graceful && socket.readyState === WebSocket.OPEN) {
          this.setContext(id, { status: "processing" });
          socket.send(JSON.stringify({ type: "stop" }));
          runtimeJobs.delete(id);
          await waitForSocketClose(socket, 120000);
        } else {
          socket.close();
        }
      } finally {
        if (socket.readyState === WebSocket.OPEN) socket.close();
        if (!graceful && this.contexts[id]?.status !== "error") {
          this.setContext(id, { status: "idle" });
        }
        this.cleanupRuntime(id, job);
      }
    },
    cleanupRuntime(id: string, expectedJob?: VoiceRuntimeJob) {
      const job = runtimeJobs.get(id);
      if (expectedJob && job && job !== expectedJob) return;
      job?.stream?.getTracks().forEach((track) => track.stop());
      if (!expectedJob || job === expectedJob) runtimeJobs.delete(id);
      if (this.activeRecordingContextId === id && (!expectedJob || job === expectedJob)) this.activeRecordingContextId = "";
    },
  },
});
