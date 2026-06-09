<script setup lang="ts">
import { computed, ref } from "vue";
import VoiceTextarea from "./VoiceTextarea.vue";

const props = defineProps<{
  modelValue: string;
  voiceContextId: string;
  placeholder: string;
  status?: string;
  stopping?: boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: string];
  "queue-prompt": [];
  "clear-prompt": [];
  "stop-run": [];
}>();

const textarea = ref<InstanceType<typeof VoiceTextarea> | null>(null);

const promptText = computed({
  get: () => props.modelValue,
  set: (value: string) => emit("update:modelValue", value),
});
const canQueue = computed(() => Boolean(props.modelValue.trim()));
const isRunning = computed(() => props.status === "running");

function handleKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" || (!event.metaKey && !event.ctrlKey)) return;
  event.preventDefault();
  emit("queue-prompt");
}
</script>

<template>
  <form class="agent-composer" @submit.prevent="emit('queue-prompt')">
    <VoiceTextarea
      ref="textarea"
      v-model="promptText"
      :context-id="voiceContextId"
      :placeholder="placeholder"
      :clearable="true"
      :trailing-actions="true"
      @keydown="handleKeydown"
      @clear="emit('clear-prompt')"
    >
      <template #actions>
        <button
          class="btn btn-outline-primary voice-action-button"
          type="button"
          :disabled="!canQueue"
          title="Queue message (Cmd/Ctrl+Enter)"
          aria-label="Queue message"
          @click="emit('queue-prompt')"
        >
          <i class="bi bi-send"></i>
        </button>
      </template>
      <template #trailing-actions>
        <button
          class="btn btn-outline-danger voice-action-button"
          type="button"
          :disabled="!isRunning || stopping"
          :title="stopping ? 'Stopping run' : 'Stop run'"
          :aria-label="stopping ? 'Stopping run' : 'Stop run'"
          @click="emit('stop-run')"
        >
          <i class="bi bi-stop-fill"></i>
        </button>
      </template>
    </VoiceTextarea>
  </form>
</template>

<style scoped>
.agent-composer {
  background: #ffffff;
  border-top: 1px solid var(--border);
  display: grid;
  flex: 0 0 auto;
  gap: 7px;
  padding: 8px;
}
</style>
