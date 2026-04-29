<script setup lang="ts">
import { computed, onUnmounted, ref } from "vue";
import { voiceSocketUrl } from "../api/client";

const model = defineModel<string>({ required: true });
const emit = defineEmits<{ start: [] }>();

type VoiceMessage = { type: "ready" | "partial" | "final" | "error"; text?: string; message?: string };

const connecting = ref(false);
const recording = ref(false);
const error = ref("");

let voiceSocket: WebSocket | null = null;
let voiceRecorder: MediaRecorder | null = null;
let voiceStream: MediaStream | null = null;
let baseText = "";
let ready = false;

const active = computed(() => connecting.value || recording.value);
const icon = computed(() => {
  if (error.value) return "bi-exclamation-triangle-fill";
  if (connecting.value) return "bi-hourglass-split";
  if (recording.value) return "bi-record-circle-fill";
  return "bi-mic-fill";
});
const title = computed(() => {
  if (error.value) return error.value;
  if (connecting.value) return "Connecting voice input";
  if (recording.value) return "Stop voice input";
  return "Start voice input";
});
const buttonClass = computed(() => {
  if (error.value) return "btn-outline-danger";
  if (recording.value) return "btn-danger";
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

function stopVoiceInput() {
  connecting.value = false;
  recording.value = false;
  ready = false;
  if (voiceRecorder && voiceRecorder.state !== "inactive") {
    voiceRecorder.stop();
  }
  voiceRecorder = null;
  voiceStream?.getTracks().forEach((track) => track.stop());
  voiceStream = null;
  if (voiceSocket?.readyState === WebSocket.OPEN) {
    voiceSocket.send(JSON.stringify({ type: "stop" }));
  }
  voiceSocket?.close();
  voiceSocket = null;
  baseText = model.value;
}

async function startVoiceInput() {
  if (recording.value) return;
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
    const mimeType = supportedVoiceMimeType();
    voiceSocket = new WebSocket(voiceSocketUrl());
    voiceSocket.binaryType = "arraybuffer";
    voiceSocket.addEventListener("message", (event) => {
      const message = JSON.parse(event.data) as VoiceMessage;
      if (message.type === "ready") {
        ready = true;
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
        stopVoiceInput();
      }
    });
    voiceSocket.addEventListener("close", () => {
      if (active.value) stopVoiceInput();
    });
    voiceSocket.addEventListener("error", () => {
      error.value = "Voice input connection failed.";
      stopVoiceInput();
    });

    voiceRecorder = new MediaRecorder(voiceStream, mimeType ? { mimeType } : undefined);
    voiceRecorder.addEventListener("dataavailable", async (event) => {
      if (ready && event.data.size > 0 && voiceSocket?.readyState === WebSocket.OPEN) {
        voiceSocket.send(await event.data.arrayBuffer());
      }
    });
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    stopVoiceInput();
  }
}

function toggleVoiceInput() {
  if (active.value) {
    stopVoiceInput();
  } else {
    void startVoiceInput();
  }
}

onUnmounted(stopVoiceInput);

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
