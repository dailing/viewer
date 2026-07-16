<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useVoiceStore } from "../stores/voice";

const model = defineModel<string>({ required: true });
const props = defineProps<{ contextId: string }>();
const emit = defineEmits<{ start: [] }>();

const error = ref("");
const voiceButton = ref<HTMLButtonElement | null>(null);
const voice = useVoiceStore();
const applyingStoreText = ref(false);
const state = computed(() => voice.context(props.contextId));
const shouldSyncModelToVoice = computed(() => state.value.status !== "idle" || Boolean(state.value.text));
const icon = computed(() => {
  if (error.value || state.value.status === "error") return "bi-exclamation-triangle-fill";
  if (state.value.status === "connecting" || state.value.status === "processing") return "bi-hourglass-split";
  if (state.value.status === "recording") return "bi-record-circle-fill";
  if (state.value.status === "ready") return "bi-check-circle-fill";
  return "bi-mic-fill";
});
const title = computed(() => {
  if (error.value || state.value.status === "error") return error.value || state.value.error;
  if (state.value.status === "connecting") return "Connecting voice input";
  if (state.value.status === "processing") return "Processing voice input";
  if (state.value.status === "recording") return "Stop voice input";
  if (state.value.status === "ready") return "Voice text ready";
  return "Start voice input";
});
const buttonClass = computed(() => {
  if (error.value || state.value.status === "error") return "btn-outline-danger";
  if (state.value.status === "recording") return "btn-danger";
  if (state.value.status === "processing" || state.value.status === "ready") return "btn-warning";
  if (state.value.status === "connecting") return "btn-outline-primary";
  return "btn-outline-secondary";
});

async function startVoiceInput() {
  emit("start");
  error.value = "";
  try {
    await voice.start(props.contextId, model.value);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function toggleVoiceInput() {
  if (state.value.status === "connecting" || state.value.status === "recording") {
    void voice.stop(props.contextId);
  } else {
    void startVoiceInput();
  }
}

function focusVoiceButton() {
  voiceButton.value?.focus();
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
    if (applyingStoreText.value) return;
    if (!shouldSyncModelToVoice.value) return;
    voice.syncText(props.contextId, text);
  },
  { immediate: true },
);

defineExpose({ focus: focusVoiceButton, stop: () => voice.stop(props.contextId) });
</script>

<template>
  <div class="voice-input-control">
    <button
      ref="voiceButton"
      class="btn btn-sm voice-action-button voice-input-button"
      :class="buttonClass"
      type="button"
      :title="title"
      :aria-label="title"
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
  gap: 4px;
}

.voice-input-button {
  align-items: center;
  display: inline-flex;
  justify-content: center;
}

</style>
