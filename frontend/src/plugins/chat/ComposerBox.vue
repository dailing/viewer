<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import VoiceInputButton from "../voice/VoiceInputButton.vue";
import { useVoiceStore } from "../voice/voiceStore";
import type { Role } from "./types";

const props = defineProps<{
  selectedRoleIds: string[];
  roles: Role[];
  contextId: string;
}>();

const emit = defineEmits<{
  "update:selectedRoleIds": [value: string[]];
  send: [value: string, forceNewSession: boolean];
}>();

// The draft text lives inside the composer (not the pane), so keystrokes only
// re-render this small component instead of the whole timeline. `send` carries
// the final text up.
const draft = ref("");

// One-shot new-session toggle: when armed, the next send carries
// force_new_session=true (each selected role starts a fresh agent session
// instead of resuming the stored one) and the toggle resets itself.
const forceNewSession = ref(false);

const voice = useVoiceStore();
const voiceError = computed(() => {
  const state = voice.context(props.contextId);
  return state.status === "error" ? state.error : "";
});

const textarea = ref<HTMLTextAreaElement | null>(null);
const selectedRoles = computed(() => {
  const selected = new Set(props.selectedRoleIds);
  return props.roles.filter((role) => selected.has(role.id));
});
const dispatchPickerTitle = computed(() =>
  selectedRoles.value.length > 0
    ? `Dispatch to ${selectedRoles.value.map((role) => role.name).join(", ")}`
    : "Auto route",
);

watch(
  draft,
  () => resizeTextarea(),
);

onMounted(() => resizeTextarea());

// Grow the textarea only after a pause, never mid-keystroke: resizing on every
// input forces a full-document layout (~150-200ms with thousands of messages
// in the DOM), which made typing freeze. While the user types continuously the
// height stays fixed (overflow scrolls), then it auto-grows once they pause.
let resizeTimer: number | undefined;
function resizeTextarea(): void {
  window.clearTimeout(resizeTimer);
  resizeTimer = window.setTimeout(() => {
    const element = textarea.value;
    if (element === null) return;
    element.style.height = "auto";
    element.style.height = `${element.scrollHeight}px`;
  }, 300);
}

function clearText(): void {
  if (draft.value === "") return;
  draft.value = "";
  void nextTick(() => textarea.value?.focus());
}

// Send takes a snapshot of the draft, clears the box, and refocuses it.
// Clearing after send prevents duplicate sends: the previous code emitted the
// raw `draft` ref from both send paths and never reset it, so the message
// stayed in the box and a second click re-sent it (chat 9475ce5db012 has 3
// identical user rows from that). Empty/whitespace input is a no-op.
function handleSend(): void {
  const text = draft.value;
  if (text.trim() === "") return;
  emit("send", text, forceNewSession.value);
  forceNewSession.value = false;
  draft.value = "";
  void nextTick(() => textarea.value?.focus());
}

function clearRoles(): void {
  emit("update:selectedRoleIds", []);
}

function toggleRole(roleId: string): void {
  const next = new Set(props.selectedRoleIds);
  if (next.has(roleId)) next.delete(roleId);
  else next.add(roleId);
  emit("update:selectedRoleIds", [...next]);
}

function isRoleSelected(roleId: string): boolean {
  return props.selectedRoleIds.includes(roleId);
}

function handleSummaryClick(event: MouseEvent): void {
  if (selectedRoles.value.length === 0) return;
  event.preventDefault();
  clearRoles();
  const details = event.currentTarget instanceof HTMLElement
    ? event.currentTarget.closest("details")
    : null;
  if (details instanceof HTMLDetailsElement) details.open = false;
}

function closePicker(event: Event): void {
  if (event.currentTarget instanceof HTMLDetailsElement) event.currentTarget.open = false;
}

function handlePickerFocusOut(event: FocusEvent): void {
  const details = event.currentTarget;
  if (!(details instanceof HTMLDetailsElement)) return;
  const nextTarget = event.relatedTarget instanceof Node ? event.relatedTarget : null;
  if (nextTarget !== null && details.contains(nextTarget)) return;
  window.setTimeout(() => {
    const active = document.activeElement;
    if (active !== null && details.contains(active)) return;
    details.open = false;
  }, 0);
}
</script>

<template>
  <div class="composer-card">
    <textarea
      ref="textarea"
      v-model="draft"
      rows="2"
      placeholder="Message"
      @input="resizeTextarea"
      @keydown.ctrl.enter.prevent="handleSend"
    />
    <div v-if="voiceError !== ''" class="voice-error">
      <i class="bi bi-exclamation-triangle" />
      <span class="voice-error-text" :title="voiceError">{{ voiceError }}</span>
      <button
        class="voice-error-dismiss"
        type="button"
        title="Dismiss"
        aria-label="Dismiss voice error"
        @click="voice.dismissError(contextId)"
      >
        <i class="bi bi-x" />
      </button>
    </div>
    <div class="composer-actions">
      <div class="composer-actions-main">
        <VoiceInputButton v-model="draft" :context-id="contextId" />
        <details
          class="dispatch-picker"
          :class="{ active: selectedRoles.length > 0 }"
          :title="dispatchPickerTitle"
          @focusout="handlePickerFocusOut"
          @keydown.esc.stop.prevent="closePicker"
        >
          <summary :aria-label="dispatchPickerTitle" @click="handleSummaryClick">
            <i class="bi" :class="selectedRoles.length > 0 ? 'bi-people-fill' : 'bi-diagram-3'" />
            <span class="dispatch-label">
              {{ selectedRoles.length > 0 ? selectedRoles.map((role) => role.name).join(", ") : "Auto" }}
            </span>
            <span v-if="selectedRoles.length > 1" class="dispatch-count">{{ selectedRoles.length }}</span>
          </summary>
          <div class="list-group dispatch-menu" @mousedown.prevent>
            <button
              class="list-group-item list-group-item-action dispatch-option"
              :class="{ selected: selectedRoles.length === 0 }"
              type="button"
              @click="clearRoles"
            >
              <i class="bi" :class="selectedRoles.length === 0 ? 'bi-check-circle-fill' : 'bi-circle'" />
              <span>Auto</span>
            </button>
            <div v-if="roles.length === 0" class="dispatch-empty">No roles in chat</div>
            <button
              v-for="role in roles"
              :key="role.id"
              class="list-group-item list-group-item-action dispatch-option"
              :class="{ selected: isRoleSelected(role.id) }"
              type="button"
              @click="toggleRole(role.id)"
            >
              <i class="bi" :class="isRoleSelected(role.id) ? 'bi-check-square-fill' : 'bi-square'" />
              <span>{{ role.name }}</span>
            </button>
          </div>
        </details>
        <button
          class="btn btn-sm btn-outline-secondary action-button"
          :class="{ active: forceNewSession }"
          type="button"
          :title="forceNewSession ? 'New session armed: next send starts a fresh agent session' : 'Start new session on next send (one-shot)'"
          :aria-pressed="forceNewSession"
          aria-label="Start new session on next send"
          @click="forceNewSession = !forceNewSession"
        >
          <i class="bi" :class="forceNewSession ? 'bi-plus-square-fill' : 'bi-plus-square'" />
        </button>
        <button class="btn btn-sm btn-primary action-button" type="button" title="Dispatch (Ctrl+Enter)" aria-label="Dispatch message" @click="handleSend">
          <i class="bi bi-send" />
        </button>
      </div>
      <div class="composer-actions-trailing">
        <button class="btn btn-sm btn-outline-secondary action-button" type="button" title="Clear text" aria-label="Clear text" :disabled="draft === ''" @click="clearText">
          <i class="bi bi-eraser" />
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.composer-card {
  background: var(--color-surface-muted, var(--bs-tertiary-bg));
  border: 0;
  border-radius: var(--radius-md, 2px);
  display: grid;
  gap: 3px;
  min-width: 0;
  padding: 3px;
  width: 100%;
}

.composer-card textarea {
  background: var(--bs-body-bg);
  border: 1px solid var(--bs-border-color);
  border-radius: var(--radius-sm, 2px);
  color: var(--bs-body-color);
  font-family: inherit;
  font-size: var(--font-size-content, 13px);
  line-height: 1.35;
  max-height: 50vh;
  min-height: 58px;
  outline: none;
  overflow: auto;
  padding: 8px;
  resize: vertical;
  width: 100%;
}

.composer-card textarea:focus {
  border-color: var(--bs-border-color);
  box-shadow: none;
}

/* Voice failure notice: single low-key line (no box), per activity-row ruling. */
.voice-error {
  align-items: center;
  color: var(--bs-danger-text-emphasis, var(--bs-danger));
  display: flex;
  font-size: 11px;
  gap: 6px;
  line-height: 1.3;
  min-height: 18px;
  padding: 0 4px;
}

.voice-error-text {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.voice-error-dismiss {
  background: transparent;
  border: 0;
  color: inherit;
  cursor: pointer;
  flex: 0 0 auto;
  font-size: 13px;
  line-height: 1;
  opacity: 0.7;
  padding: 0 2px;
}

.voice-error-dismiss:hover {
  opacity: 1;
}

.composer-actions,
.composer-actions-main,
.composer-actions-trailing {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.composer-actions-main {
  flex: 1 1 auto;
  min-width: 0;
}

.composer-actions-trailing {
  border-left: 1px solid var(--bs-border-color);
  flex: 0 0 auto;
  margin-left: auto;
  padding-left: 10px;
}

.composer-actions :deep(.btn),
.dispatch-picker summary {
  align-items: center;
  display: inline-flex;
  justify-content: center;
  white-space: nowrap;
}

.composer-actions :deep(.voice-input-button),
.action-button {
  flex: 0 0 auto;
  height: 32px;
  min-width: 0;
  padding: 0;
  width: 32px;
}

.action-button.active {
  background: var(--color-surface-selected, var(--bs-secondary-bg));
  color: var(--bs-body-color);
}

.composer-actions :deep(.bi) {
  font-size: 14px;
  line-height: 1;
}

.dispatch-picker {
  display: inline-block;
  flex: 0 0 auto;
  height: 32px;
  margin: 0;
  max-width: min(220px, 34vw);
  padding: 0;
  position: relative;
}

.dispatch-picker summary {
  background: transparent;
  border: 1px solid var(--bs-border-color);
  border-radius: var(--radius-sm, 2px);
  color: var(--bs-secondary-color);
  cursor: pointer;
  gap: 5px;
  height: 32px;
  list-style: none;
  max-width: min(220px, 34vw);
  min-width: 58px;
  padding: 0 8px;
  position: relative;
}

.dispatch-picker summary::-webkit-details-marker {
  display: none;
}

.dispatch-label {
  font-size: 11px;
  font-weight: 700;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dispatch-count {
  align-items: center;
  background: transparent;
  border-radius: 999px;
  color: var(--bs-secondary-color);
  display: inline-flex;
  font-size: 9px;
  height: 14px;
  justify-content: center;
  line-height: 1;
  min-width: 14px;
  padding: 0 4px;
  position: absolute;
  right: -4px;
  top: -5px;
}

.dispatch-menu {
  --bs-list-group-action-hover-bg: var(--color-surface-hover, var(--bs-tertiary-bg));
  --bs-list-group-action-hover-color: var(--bs-body-color);
  --bs-list-group-bg: transparent;
  --bs-list-group-border-radius: 0;
  --bs-list-group-border-width: 0;
  --bs-list-group-color: var(--bs-body-color);
  background: var(--color-surface-raised, var(--bs-body-bg));
  border: 0;
  border-radius: var(--radius-md, 2px);
  bottom: calc(100% + 6px);
  display: flex;
  gap: 3px;
  left: 0;
  max-height: min(260px, 42vh);
  min-width: 180px;
  overflow-y: auto;
  padding: 6px;
  position: absolute;
  z-index: 30;
}

.dispatch-option {
  align-items: center;
  display: flex;
  font-size: 12px;
  gap: 7px;
  min-width: 0;
  padding: 6px 7px;
  text-align: left;
  width: 100%;
}

.dispatch-option.selected {
  color: var(--color-accent-hover, var(--bs-primary));
  font-weight: 700;
}

.dispatch-option span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dispatch-empty {
  color: var(--bs-secondary-color);
  font-size: 12px;
  padding: 7px;
}
</style>
