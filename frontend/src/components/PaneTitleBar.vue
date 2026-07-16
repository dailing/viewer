<script setup lang="ts">
import { computed } from "vue";
import { useInputSessionsStore } from "../stores/inputSessions";
import { useLayoutStore } from "../stores/layout";
import { usePaneToolbarStore } from "../stores/paneToolbar";
import type { PaneToolbarAction, PaneToolbarControl } from "../stores/paneToolbar";
import type { LayoutNode, SplitDirection } from "../types/layout";

const props = defineProps<{ pane: Extract<LayoutNode, { type: "pane" }> }>();
const inputSessions = useInputSessionsStore();
const layout = useLayoutStore();
const paneToolbar = usePaneToolbarStore();

const toolbar = computed(() => paneToolbar.forPane(props.pane.id));
const isActive = computed(() => layout.activePaneId === props.pane.id);
const hasContent = computed(() => Boolean(props.pane.filePath || props.pane.terminalId || props.pane.diffPath || props.pane.chatId));
const canGoBack = computed(() => Boolean(props.pane.history?.length));
const title = computed(() => {
  if (toolbar.value?.title) return toolbar.value.title;
  if (props.pane.chatId) return "Chat";
  if (props.pane.terminalId) return "Terminal";
  if (props.pane.diffPath) return `Diff: ${props.pane.diffPath}`;
  return props.pane.filePath || "Empty pane";
});
const inputStatus = computed(() => inputSessions.globalStatus);

function activate() {
  layout.setActive(props.pane.id);
}

function runAction(action: PaneToolbarAction) {
  activate();
  void action.run();
}

function updateControl(control: PaneToolbarControl, event: Event) {
  if (control.kind !== "select") return;
  const target = event.target as HTMLSelectElement | null;
  if (!target) return;
  activate();
  void control.onChange(target.value);
}

function refreshPane() {
  activate();
  window.dispatchEvent(new CustomEvent("viewer:pane-refresh", { detail: { paneId: props.pane.id } }));
}

function splitPane(direction: SplitDirection) {
  layout.splitPane(props.pane.id, direction);
}

function goBack() {
  activate();
  layout.goBack(props.pane.id);
}

function closePane() {
  activate();
  if (hasContent.value) {
    layout.clearPane(props.pane.id);
    return;
  }
  layout.closePane(props.pane.id);
}

function sendGlobalInput() {
  void inputSessions.requestGlobalSend();
}
</script>

<template>
  <header class="pane-titlebar" :class="{ active: isActive }" @pointerdown="activate">
    <div class="pane-title" :title="title">{{ title }}</div>
    <span v-if="toolbar?.status" class="pane-status" :class="toolbar.statusClass">{{ toolbar.status }}</span>

    <div v-if="isActive && inputStatus.visible" class="global-input-status" :class="`status-${inputStatus.status}`">
      <i class="bi" :class="inputStatus.busy ? 'bi-mic-fill' : inputStatus.status === 'failed' ? 'bi-exclamation-triangle-fill' : 'bi-check-circle-fill'"></i>
      <span class="global-input-status-text" :title="`${inputStatus.label}: ${inputStatus.detail}`">
        {{ inputStatus.detail || inputStatus.label }}
      </span>
      <button class="btn btn-sm btn-primary global-input-send" type="button" :disabled="!inputStatus.canSend" title="Finish voice input" @pointerdown.stop @click.stop="sendGlobalInput">
        <i class="bi bi-send"></i>
      </button>
    </div>

    <div class="pane-titlebar-tools">
      <div v-if="toolbar?.actions?.length" class="pane-titlebar-actions" aria-label="Pane-specific actions">
        <button
          v-for="action in toolbar.actions"
          :key="action.id"
          class="btn icon-button pane-titlebar-action"
          :class="[{ active: action.active, 'has-label': action.label }, action.variant === 'danger' ? 'danger' : '']"
          type="button"
          :title="action.title"
          :aria-label="action.title"
          @pointerdown.stop
          @click.stop="runAction(action)"
        >
          <i v-if="action.icon" class="bi" :class="action.icon"></i>
          <span v-else>{{ action.label }}</span>
        </button>
      </div>

      <template v-for="control in toolbar?.controls ?? []" :key="control.id">
        <select
          v-if="control.kind === 'select'"
          class="form-select form-select-sm pane-titlebar-select"
          :class="{ compact: control.size === 'compact' }"
          :title="control.title"
          :value="control.value"
          @pointerdown.stop
          @change="updateControl(control, $event)"
        >
          <option v-for="option in control.options" :key="option" :value="option">{{ option }}</option>
        </select>
        <div v-else class="pane-titlebar-chips" :title="control.title">
          <span v-for="(item, index) in control.items" :key="`${index}:${item}`" class="pane-titlebar-chip">{{ item }}</span>
        </div>
      </template>

      <div class="pane-titlebar-actions pane-layout-actions" aria-label="Pane layout actions">
        <button class="btn icon-button pane-titlebar-action" type="button" title="Refresh pane" @pointerdown.stop @click.stop="refreshPane">
          <i class="bi bi-arrow-clockwise"></i>
        </button>
        <button v-if="canGoBack" class="btn icon-button pane-titlebar-action" type="button" title="Go back" @pointerdown.stop @click.stop="goBack">
          <i class="bi bi-arrow-left"></i>
        </button>
        <button class="btn icon-button pane-titlebar-action" type="button" title="Split pane right" @pointerdown.stop @click.stop="splitPane('vertical')">
          <i class="bi bi-layout-split"></i>
        </button>
        <button class="btn icon-button pane-titlebar-action" type="button" title="Split pane down" @pointerdown.stop @click.stop="splitPane('horizontal')">
          <i class="bi bi-view-stacked"></i>
        </button>
        <button class="btn icon-button pane-titlebar-action danger" type="button" :title="hasContent ? 'Clear pane content' : 'Close empty pane'" @pointerdown.stop @click.stop="closePane">
          <i class="bi bi-x-lg"></i>
        </button>
      </div>
    </div>
  </header>
</template>

<style scoped>
.pane-titlebar {
  align-items: center;
  background: var(--color-surface-selected);
  display: flex;
  flex: 0 0 var(--pane-titlebar-height);
  gap: 3px;
  min-width: 0;
  overflow: hidden;
  padding: 2px 3px 2px var(--pane-content-inline-padding);
}

.pane-titlebar.active {
  background: var(--color-surface-selected);
  box-shadow: inset 0 1px 0 var(--color-accent);
}

.pane-title {
  flex: 1 1 auto;
  font-size: var(--font-size-ui-small);
  font-weight: 600;
  min-width: 48px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pane-titlebar-actions,
.pane-titlebar-chips {
  align-items: center;
  display: flex;
  flex: 0 0 auto;
  gap: 2px;
}

.pane-titlebar-tools {
  align-items: center;
  display: flex;
  flex: 0 0 auto;
  gap: 3px;
  margin-left: auto;
}

.pane-titlebar-action {
  background: transparent;
  border-color: transparent;
  color: var(--color-text-muted);
  height: var(--nav-button-size);
  min-width: var(--nav-button-size);
  padding: 0;
  width: var(--nav-button-size);
}

.pane-titlebar-action:hover,
.pane-titlebar-action:active,
.pane-titlebar-action:focus-visible {
  background: transparent;
  border-color: transparent;
  color: var(--color-text);
}

.pane-titlebar-action.has-label {
  font-size: 10px;
  font-weight: 700;
  width: 32px;
}

.pane-titlebar-action.active {
  background: transparent;
  color: var(--color-text);
}

.pane-titlebar-action.danger {
  color: var(--color-danger);
}

.pane-titlebar-select {
  flex: 0 0 120px;
  font-size: var(--font-size-ui-small);
  height: var(--nav-button-size);
  min-width: 80px;
  padding: 0 18px 0 5px;
}

.pane-titlebar-select.compact {
  flex-basis: 64px;
  min-width: 54px;
}

.pane-titlebar-chips {
  max-width: 180px;
  overflow: hidden;
}

.pane-titlebar-chip {
  background: var(--color-surface);
  color: var(--color-text-muted);
  font-size: 10px;
  overflow: hidden;
  padding: 2px 5px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pane-layout-actions {
  margin-left: 2px;
}

@media (max-width: 767.98px) {
  .pane-titlebar {
    overflow-x: auto;
    scrollbar-width: none;
  }

  .pane-titlebar::-webkit-scrollbar {
    display: none;
  }

  .pane-title {
    position: sticky;
    left: 0;
  }
}
</style>
