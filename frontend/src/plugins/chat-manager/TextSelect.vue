<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";

defineProps<{
  label: string;
  modelValue: string;
  options: Array<{ value: string; label: string; disabled?: boolean }>;
}>();

const emit = defineEmits<{ "update:modelValue": [value: string] }>();
const open = ref(false);

function select(value: string, disabled = false): void {
  if (disabled) return;
  emit("update:modelValue", value);
  open.value = false;
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") open.value = false;
}

onMounted(() => window.addEventListener("keydown", handleKeydown));
onBeforeUnmount(() => window.removeEventListener("keydown", handleKeydown));
</script>

<template>
  <div class="text-select">
    <div class="text-select-label">{{ label }}</div>
    <button type="button" class="text-select-value" :title="modelValue || 'Default'" @click="open = !open">
      {{ options.find((option) => option.value === modelValue)?.label ?? modelValue ?? "Default" }}
    </button>
    <div v-if="open" class="text-select-overlay" @click="open = false"></div>
    <div v-if="open" class="text-select-menu">
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        class="text-select-option"
        :class="{ selected: option.value === modelValue }"
        :disabled="option.disabled"
        @click="select(option.value, option.disabled)"
      >
        {{ option.label }}
      </button>
      <div v-if="options.length === 0" class="text-select-empty">没有可用选项</div>
    </div>
  </div>
</template>

<style scoped>
.text-select {
  min-width: 0;
  position: relative;
}

.text-select-label {
  color: var(--color-text-subtle);
  font-size: var(--font-size-ui-small);
  line-height: 1.2;
}

.text-select-value {
  background: transparent;
  border: 0;
  color: var(--color-text);
  display: block;
  overflow: hidden;
  padding: 3px 0;
  text-align: left;
  text-overflow: ellipsis;
  white-space: nowrap;
  width: 100%;
}

.text-select-value:hover {
  color: var(--color-accent);
}

.text-select-overlay {
  inset: 0;
  position: fixed;
  z-index: 50;
}

.text-select-menu {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  left: 0;
  max-height: 220px;
  min-width: 150px;
  overflow-y: auto;
  padding: 3px;
  position: absolute;
  top: 100%;
  z-index: 51;
}

.text-select-option {
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text);
  display: block;
  padding: 5px 7px;
  text-align: left;
  white-space: nowrap;
  width: 100%;
}

.text-select-option:hover:not(:disabled),
.text-select-option.selected {
  background: var(--color-surface-hover);
}

.text-select-option:disabled,
.text-select-empty {
  color: var(--color-text-subtle);
}

.text-select-empty {
  padding: 5px 7px;
}
</style>
