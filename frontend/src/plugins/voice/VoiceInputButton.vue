<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";

import { useVoiceStore } from "./voiceStore";

const model = defineModel<string>({ required: true });
const props = defineProps<{ contextId: string }>();
const emit = defineEmits<{ start: [] }>();

const error = ref("");
const voiceButton = ref<HTMLButtonElement | null>(null);
const voice = useVoiceStore();
const applyingStoreText = ref(false);
const state = computed(() => voice.context(props.contextId));
const shouldSyncModelToVoice = computed(
  () => state.value.status !== "idle" || Boolean(state.value.text),
);
const icon = computed(() => {
  if (error.value !== "" || state.value.status === "error")
    return "bi-exclamation-triangle-fill";
  if (state.value.status === "connecting" || state.value.status === "processing")
    return "bi-hourglass-split";
  if (state.value.status === "recording") return "bi-record-circle-fill";
  if (state.value.status === "ready") return "bi-check-circle-fill";
  return "bi-mic-fill";
});
const title = computed(() => {
  if (!voice.backendAvailable) return "语音不可用：voice 插件未运行";
  if (error.value !== "" || state.value.status === "error")
    return error.value || state.value.error;
  if (state.value.status === "connecting") return "Connecting voice input";
  if (state.value.status === "processing") return "Processing voice input";
  if (state.value.status === "recording") return "Stop voice input";
  if (state.value.status === "ready") return "Voice text ready";
  return "Start voice input";
});
const buttonClass = computed(() => {
  if (error.value !== "" || state.value.status === "error") return "btn-outline-danger";
  if (state.value.status === "recording") return "btn-danger";
  if (state.value.status === "processing" || state.value.status === "ready")
    return "btn-warning";
  if (state.value.status === "connecting") return "btn-outline-primary";
  return "btn-outline-secondary";
});

async function startVoiceInput(): Promise<void> {
  emit("start");
  error.value = "";
  try {
    await voice.start(props.contextId, model.value);
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
  }
}

function toggleVoiceInput(): void {
  if (!voice.backendAvailable) return;
  if (state.value.status === "connecting" || state.value.status === "recording") {
    void voice.stop(props.contextId);
  } else {
    void startVoiceInput();
  }
}

watch(
  () => state.value.text,
  (text) => {
    if (text === model.value) return;
    applyingStoreText.value = true;
    model.value = text;
    applyingStoreText.value = false;
  },
);

watch(
  model,
  (text) => {
    if (applyingStoreText.value || !shouldSyncModelToVoice.value) return;
    voice.syncText(props.contextId, text);
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  if (["connecting", "recording", "processing"].includes(state.value.status)) {
    void voice.cancel(props.contextId);
  }
});

defineExpose({
  focus: () => voiceButton.value?.focus(),
  stop: () => voice.stop(props.contextId),
});
</script>

<template>
  <div class="voice-input-control">
    <button
      ref="voiceButton"
      class="btn btn-sm voice-input-button"
      :class="[buttonClass, { 'voice-input-button--unavailable': !voice.backendAvailable }]"
      type="button"
      :title="title"
      :aria-label="title"
      :disabled="!voice.backendAvailable"
      @click="toggleVoiceInput"
    >
      <i class="bi" :class="icon"></i>
    </button>
  </div>
</template>

<style scoped>
.voice-input-control {
  display: inline-flex;
  flex: 0 0 auto;
}

.voice-input-button {
  align-items: center;
  display: inline-flex;
  justify-content: center;
}

.voice-input-button--unavailable {
  filter: grayscale(1);
  opacity: 0.45;
}
</style>
