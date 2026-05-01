<script setup lang="ts">
import { computed, onUnmounted, ref } from "vue";
import { voiceSocketUrl } from "../api/client";

const model = defineModel<string>({ required: true });
const emit = defineEmits<{ start: [] }>();

type VoiceMessage = { type: "ready" | "partial" | "final" | "error"; text?: string; message?: string };

const connecting = ref(false);
const recording = ref(false);
const stopping = ref(false);
const error = ref("");

let voiceSocket: WebSocket | null = null;
let voiceRecorder: MediaRecorder | null = null;
let voiceStream: MediaStream | null = null;
let baseText = "";
let ready = false;
let selectedMimeType = "";
let pendingChunkSends: Promise<void>[] = [];

const active = computed(() => connecting.value || recording.value || stopping.value);
const icon = computed(() => {
  if (error.value) return "bi-exclamation-triangle-fill";
  if (connecting.value || stopping.value) return "bi-hourglass-split";
  if (recording.value) return "bi-record-circle-fill";
  return "bi-mic-fill";
});
const title = computed(() => {
  if (error.value) return error.value;
  if (connecting.value) return "Connecting voice input";
  if (stopping.value) return "Finishing voice input";
  if (recording.value) return "Stop voice input";
  return "Start voice input";
});
const buttonClass = computed(() => {
  if (error.value) return "btn-outline-danger";
  if (recording.value || stopping.value) return "btn-danger";
  if (connecting.value) return "btn-outline-primary";
  return "btn-outline-secondary";
});

function appendVoiceText(text: string) {
  if (!text) return;
  const separator = baseText && !/\s$/.test(baseText) ? " " : "";
  baseText = `${baseText}${separator}${text}`;
  model.value = baseText;
}

function applyVoicePartial(text: string) {
  const separator = baseText && text && !/\s$/.test(baseText) ? " " : "";
  model.value = `${baseText}${separator}${text}`;
}

function supportedVoiceMimeType(): string | undefined {
  const candidates = ["audio/webm;codecs=opus", "audio/webm", "audio/mp4"];
  return candidates.find((candidate) => MediaRecorder.isTypeSupported(candidate));
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

async function sendVoiceChunk(blob: Blob) {
  if (!ready || blob.size <= 0 || voiceSocket?.readyState !== WebSocket.OPEN) return;
  const socket = voiceSocket;
  const data = await blob.arrayBuffer();
  if (socket.readyState === WebSocket.OPEN) {
    socket.send(data);
  }
}

function cleanupVoiceInput() {
  connecting.value = false;
  recording.value = false;
  stopping.value = false;
  ready = false;
  voiceRecorder = null;
  voiceStream?.getTracks().forEach((track) => track.stop());
  voiceStream = null;
  selectedMimeType = "";
  pendingChunkSends = [];
  voiceSocket = null;
  baseText = model.value;
}

async function stopVoiceInput(graceful = true) {
  const socket = voiceSocket;
  const recorder = voiceRecorder;
  const stream = voiceStream;

  connecting.value = false;
  recording.value = false;
  stopping.value = graceful && socket?.readyState === WebSocket.OPEN;

  try {
    if (graceful && recorder) {
      await waitForRecorderStop(recorder);
      await Promise.allSettled(pendingChunkSends);
    } else if (recorder && recorder.state !== "inactive") {
      recorder.stop();
    }
    stream?.getTracks().forEach((track) => track.stop());
    if (graceful && socket?.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ type: "stop" }));
      await waitForSocketClose(socket);
    } else {
      socket?.close();
    }
  } finally {
    if (socket?.readyState === WebSocket.OPEN) {
      socket.close();
    }
    cleanupVoiceInput();
  }
}

async function startVoiceInput() {
  if (active.value) return;
  if (!navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === "undefined") {
    error.value = "Voice recording is not supported in this browser.";
    return;
  }
  emit("start");
  error.value = "";
  connecting.value = true;
  baseText = model.value;

  try {
    voiceStream = await navigator.mediaDevices.getUserMedia({
      audio: {
        channelCount: 1,
        echoCancellation: true,
        noiseSuppression: true,
      },
    });
    selectedMimeType = supportedVoiceMimeType() ?? "";
    voiceSocket = new WebSocket(voiceSocketUrl());
    voiceSocket.binaryType = "arraybuffer";
    voiceSocket.addEventListener("message", (event) => {
      const message = JSON.parse(event.data) as VoiceMessage;
      if (message.type === "ready") {
        ready = true;
        if (voiceSocket?.readyState === WebSocket.OPEN) {
          voiceSocket.send(JSON.stringify({ type: "start", mimeType: selectedMimeType }));
        }
        connecting.value = false;
        if (voiceRecorder && voiceRecorder.state === "inactive") {
          voiceRecorder.start(250);
          recording.value = true;
        }
      } else if (message.type === "partial") {
        applyVoicePartial(message.text ?? "");
      } else if (message.type === "final") {
        appendVoiceText(message.text ?? "");
      } else if (message.type === "error") {
        error.value = message.message ?? "Voice input failed.";
        void stopVoiceInput(false);
      }
    });
    voiceSocket.addEventListener("close", () => {
      if (active.value && !stopping.value) cleanupVoiceInput();
    });
    voiceSocket.addEventListener("error", () => {
      error.value = "Voice input connection failed.";
      void stopVoiceInput(false);
    });

    voiceRecorder = new MediaRecorder(voiceStream, selectedMimeType ? { mimeType: selectedMimeType } : undefined);
    voiceRecorder.addEventListener("dataavailable", (event) => {
      const send = sendVoiceChunk(event.data);
      pendingChunkSends.push(send);
      void send.finally(() => {
        pendingChunkSends = pendingChunkSends.filter((pending) => pending !== send);
      });
    });
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    void stopVoiceInput(false);
  }
}

function toggleVoiceInput() {
  if (stopping.value) return;
  if (active.value) {
    void stopVoiceInput();
  } else {
    void startVoiceInput();
  }
}

onUnmounted(() => void stopVoiceInput(false));

defineExpose({ stop: stopVoiceInput });
</script>

<template>
  <button
    class="btn btn-sm voice-input-button"
    :class="buttonClass"
    type="button"
    :title="title"
    :aria-label="title"
    @click="toggleVoiceInput"
  >
    <i class="bi" :class="icon"></i>
  </button>
</template>

<style scoped>
.voice-input-button {
  align-items: center;
  display: inline-flex;
  flex: 0 0 auto;
  justify-content: center;
  min-width: 34px;
}
</style>
