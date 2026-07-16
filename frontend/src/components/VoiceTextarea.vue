<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
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
    maxHeight?: string;
    autoGrow?: boolean;
    trailingActions?: boolean;
  }>(),
  {
    placeholder: "",
    rows: 3,
    clearable: true,
    monospace: true,
    minHeight: "92px",
    maxHeight: "50vh",
    autoGrow: false,
    trailingActions: false,
  },
);

const emit = defineEmits<{
  "update:modelValue": [value: string];
  clear: [];
  focus: [event: FocusEvent];
  blur: [event: FocusEvent];
  keydown: [event: KeyboardEvent];
}>();

const textarea = ref<HTMLTextAreaElement | null>(null);
const voiceInput = ref<InstanceType<typeof VoiceInputButton> | null>(null);
const text = computed({
  get: () => props.modelValue,
  set: (value: string) => emit("update:modelValue", value),
});
const canClear = computed(() => Boolean(props.modelValue));
const style = computed(() => ({ "--voice-textarea-min-height": props.minHeight, "--voice-textarea-max-height": props.maxHeight }));
const hasTrailingActions = computed(() => props.clearable || props.trailingActions);

watch(
  () => props.modelValue,
  () => resizeTextarea(),
);

function clearText() {
  if (!canClear.value) return;
  emit("update:modelValue", "");
  emit("clear");
  focusTextarea();
}

function focusTextarea() {
  void nextTick(() => textarea.value?.focus());
}

function focusVoiceInput() {
  void nextTick(() => voiceInput.value?.focus());
}

function resizeTextarea() {
  if (!props.autoGrow) return;
  void nextTick(() => {
    const element = textarea.value;
    if (!element) return;
    element.style.height = "auto";
    element.style.height = `${element.scrollHeight}px`;
  });
}

defineExpose({ focus: focusTextarea, focusVoice: focusVoiceInput });
</script>

<template>
  <div class="voice-textarea" :style="style">
    <textarea
      ref="textarea"
      v-model="text"
      :rows="rows"
      :placeholder="placeholder"
      :class="{ monospace }"
      @blur="emit('blur', $event)"
      @focus="emit('focus', $event)"
      @input="resizeTextarea"
      @keydown="emit('keydown', $event)"
    ></textarea>
    <div class="voice-textarea-actions">
      <div class="voice-textarea-actions-main">
        <VoiceInputButton ref="voiceInput" v-model="text" :context-id="contextId" />
        <slot name="actions"></slot>
      </div>
      <div v-if="hasTrailingActions" class="voice-textarea-actions-trailing">
        <slot name="trailing-actions"></slot>
        <button
          v-if="clearable"
          class="btn btn-sm btn-outline-secondary voice-action-button"
          type="button"
          title="Clear text"
          aria-label="Clear text"
          :disabled="!canClear"
          @click="clearText"
        >
          <i class="bi bi-eraser"></i>
        </button>
        <slot name="trailing-actions-after"></slot>
      </div>
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
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  font-size: var(--font-size-content);
  line-height: 1.35;
  max-height: var(--voice-textarea-max-height);
  min-height: var(--voice-textarea-min-height);
  outline: none;
  overflow: auto;
  padding: 8px;
  resize: vertical;
  width: 100%;
}

.voice-textarea textarea.monospace {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
}

.voice-textarea textarea:focus {
  border-color: var(--color-accent);
  box-shadow: none;
  outline: 2px solid color-mix(in srgb, var(--color-focus) 45%, transparent);
  outline-offset: 1px;
}

.voice-textarea-actions {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.voice-textarea-actions-main,
.voice-textarea-actions-trailing {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.voice-textarea-actions-main {
  flex: 1 1 auto;
  min-width: 0;
}

.voice-textarea-actions-trailing {
  border-left: 1px solid var(--color-border);
  flex: 0 0 auto;
  margin-left: auto;
  padding-left: 10px;
}

.voice-textarea-actions :deep(.btn) {
  align-items: center;
  display: inline-flex;
  gap: 6px;
  justify-content: center;
  white-space: nowrap;
}

.voice-textarea-actions :deep(.voice-action-button) {
  flex: 0 0 auto;
  gap: 0;
  height: 32px;
  min-width: 0;
  padding: 0;
  width: 32px;
}

.voice-textarea-actions :deep(.voice-action-button .bi) {
  font-size: 14px;
  line-height: 1;
}
</style>
