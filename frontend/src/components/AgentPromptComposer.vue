<script setup lang="ts">
import { computed, nextTick, ref } from "vue";
import type { AgentQueueItem } from "../types/agents";
import VoiceInputButton from "./VoiceInputButton.vue";

const props = defineProps<{
  modelValue: string;
  queue: AgentQueueItem[];
  editingQueueItemId: string | null;
  voiceContextId: string;
  placeholder: string;
  status?: string;
  stopping?: boolean;
  queueTitle?: string;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: string];
  "edit-queued": [item: AgentQueueItem];
  "delete-queued": [itemId: string];
  "save-queued": [];
  "cancel-queued": [];
  "queue-prompt": [];
  "clear-prompt": [];
  "stop-run": [];
}>();

const textarea = ref<HTMLTextAreaElement | null>(null);

const promptText = computed({
  get: () => props.modelValue,
  set: (value: string) => emit("update:modelValue", value),
});
const editingQueueItem = computed(() => props.queue.find((item) => item.id === props.editingQueueItemId) ?? null);
const isEditingQueue = computed(() => Boolean(editingQueueItem.value));
const canQueue = computed(() => Boolean(props.modelValue.trim()) && !isEditingQueue.value);
const canClearPrompt = computed(() => Boolean(props.modelValue));
const isRunning = computed(() => props.status === "running");

function focusTextarea() {
  void nextTick(() => textarea.value?.focus());
}

function editQueuedMessage(item: AgentQueueItem) {
  emit("edit-queued", item);
  focusTextarea();
}

function handleKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" || (!event.metaKey && !event.ctrlKey)) return;
  event.preventDefault();
  if (isEditingQueue.value) {
    emit("save-queued");
    return;
  }
  emit("queue-prompt");
}
</script>

<template>
  <form class="agent-composer" @submit.prevent="isEditingQueue && emit('save-queued')">
    <div v-if="queue.length" class="agent-composer-queue">
      <div class="agent-composer-queue-title">{{ queueTitle ?? "Queue" }}</div>
      <div
        v-for="(item, index) in queue"
        :key="item.id"
        class="agent-composer-queue-row"
        :class="{ active: item.id === editingQueueItemId }"
        role="button"
        tabindex="0"
        @click="editQueuedMessage(item)"
        @keydown.enter.prevent="editQueuedMessage(item)"
      >
        <span class="agent-composer-queue-index">{{ index + 1 }}</span>
        <span class="agent-composer-queue-preview">{{ item.prompt.split(/\r?\n/)[0] }}</span>
        <button class="agent-composer-queue-delete" type="button" title="Delete queued message" @click.stop="emit('delete-queued', item.id)">
          <i class="bi bi-trash"></i>
        </button>
      </div>
    </div>

    <div v-if="editingQueueItem" class="agent-composer-editing-row">Editing queued message</div>

    <div class="agent-composer-input-box">
      <textarea ref="textarea" v-model="promptText" rows="3" :placeholder="placeholder" @keydown="handleKeydown"></textarea>
    </div>

    <div class="agent-composer-actions">
      <template v-if="isEditingQueue">
        <button class="btn btn-primary" type="submit" :disabled="!modelValue.trim()">
          <i class="bi bi-check2"></i>
          <span>Save</span>
        </button>
        <button class="btn btn-outline-secondary" type="button" @click="emit('cancel-queued')">
          <i class="bi bi-x-lg"></i>
          <span>Cancel</span>
        </button>
        <button class="btn btn-outline-danger" type="button" :disabled="!editingQueueItemId" @click="editingQueueItemId && emit('delete-queued', editingQueueItemId)">
          <i class="bi bi-trash"></i>
          <span>Delete</span>
        </button>
      </template>
      <template v-else>
        <VoiceInputButton v-model="promptText" :context-id="voiceContextId" />
        <button class="btn btn-outline-primary" type="button" :disabled="!canQueue" title="Queue message (Cmd/Ctrl+Enter)" @click="emit('queue-prompt')">
          <i class="bi bi-list-ol"></i>
          <span>Queue</span>
        </button>
        <button class="btn btn-outline-secondary" type="button" :disabled="!canClearPrompt" @click="emit('clear-prompt')">
          <i class="bi bi-eraser"></i>
          <span>Clear</span>
        </button>
        <button class="btn btn-outline-danger" type="button" :disabled="!isRunning || stopping" @click="emit('stop-run')">
          <i class="bi bi-stop-fill"></i>
          <span>{{ stopping ? "Stopping" : "Stop" }}</span>
        </button>
      </template>
    </div>
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

.agent-composer-queue {
  border: 1px solid #dde5f1;
  border-radius: 6px;
  display: grid;
  gap: 2px;
  max-height: 150px;
  overflow: auto;
  padding: 5px;
}

.agent-composer-queue-title {
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 600;
  padding: 2px 4px 4px;
  text-transform: uppercase;
}

.agent-composer-queue-row {
  align-items: center;
  border-radius: 4px;
  cursor: pointer;
  display: grid;
  gap: 6px;
  grid-template-columns: 22px minmax(0, 1fr) 28px;
  min-height: 30px;
  padding: 2px 2px 2px 4px;
}

.agent-composer-queue-row:hover,
.agent-composer-queue-row.active {
  background: #edf5ff;
}

.agent-composer-queue-index {
  color: var(--text-muted);
  font-size: 11px;
  text-align: right;
}

.agent-composer-queue-preview {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-composer-queue-delete {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: 4px;
  color: #8a94a6;
  display: inline-flex;
  height: 26px;
  justify-content: center;
  padding: 0;
  width: 26px;
}

.agent-composer-queue-delete:hover {
  background: #ffe8e8;
  color: #b42318;
}

.agent-composer-editing-row {
  color: #3b4d68;
  font-size: 12px;
}

.agent-composer-input-box {
  min-width: 0;
  position: relative;
}

.agent-composer textarea {
  border: 1px solid var(--border);
  border-radius: 6px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 14px;
  line-height: 1.35;
  min-height: 92px;
  outline: none;
  padding: 8px;
  resize: vertical;
  width: 100%;
}

.agent-composer textarea:focus {
  border-color: #1f6feb;
  box-shadow: 0 0 0 2px rgb(31 111 235 / 0.16);
}

.agent-composer-actions {
  display: flex;
  gap: 6px;
}

.agent-composer-actions .btn {
  align-items: center;
  display: inline-flex;
  gap: 6px;
  justify-content: center;
  white-space: nowrap;
}
</style>
