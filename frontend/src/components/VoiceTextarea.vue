<script setup lang="ts">
import { computed, nextTick, ref } from "vue";
import VoiceInputButton from "./VoiceInputButton.vue";

const props = withDefaults(
  defineProps<{
    modelValue: string;
    contextId: string;
    placeholder?: string;
    rows?: number;
    clearable?: boolean;
    monospace?: boolean;
    minHeight?: string;
  }>(),
  {
    placeholder: "",
    rows: 3,
    clearable: true,
    monospace: true,
    minHeight: "92px",
  },
);

const emit = defineEmits<{
  "update:modelValue": [value: string];
  clear: [];
  keydown: [event: KeyboardEvent];
}>();

const textarea = ref<HTMLTextAreaElement | null>(null);
const text = computed({
  get: () => props.modelValue,
  set: (value: string) => emit("update:modelValue", value),
});
const canClear = computed(() => Boolean(props.modelValue));
const style = computed(() => ({ "--voice-textarea-min-height": props.minHeight }));

function clearText() {
  if (!canClear.value) return;
  emit("update:modelValue", "");
  emit("clear");
  focusTextarea();
}

function focusTextarea() {
  void nextTick(() => textarea.value?.focus());
}

defineExpose({ focus: focusTextarea });
</script>

<template>
  <div class="voice-textarea" :style="style">
    <textarea
      ref="textarea"
      v-model="text"
      :rows="rows"
      :placeholder="placeholder"
      :class="{ monospace }"
      @keydown="emit('keydown', $event)"
    ></textarea>
    <div class="voice-textarea-actions">
      <VoiceInputButton v-model="text" :context-id="contextId" />
      <button v-if="clearable" class="btn btn-sm btn-outline-secondary" type="button" :disabled="!canClear" @click="clearText">
        <i class="bi bi-eraser"></i>
        <span>Clear</span>
      </button>
      <slot name="actions"></slot>
    </div>
  </div>
</template>

<style scoped>
.voice-textarea {
  display: grid;
  gap: 7px;
  min-width: 0;
}

.voice-textarea textarea {
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 14px;
  line-height: 1.35;
  min-height: var(--voice-textarea-min-height);
  outline: none;
  padding: 8px;
  resize: vertical;
  width: 100%;
}

.voice-textarea textarea.monospace {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
}

.voice-textarea textarea:focus {
  border-color: #1f6feb;
  box-shadow: 0 0 0 2px rgb(31 111 235 / 0.16);
}

.voice-textarea-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.voice-textarea-actions :deep(.btn) {
  align-items: center;
  display: inline-flex;
  gap: 6px;
  justify-content: center;
  white-space: nowrap;
}
</style>
