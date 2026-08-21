<script setup lang="ts">
/**
 * One floating pane: absolutely positioned above the split tree, draggable by
 * its title bar and resizable from the bottom-right corner. Mirrors
 * PaneContainer's title logic so chrome (title/status/custom actions) behaves
 * the same in both worlds.
 */
import { computed, ref } from "vue";

import { contentUid, FLOAT_MIN_H, FLOAT_MIN_W, type FloatingPane } from "../stores/layout";
import { useLayoutStore } from "../stores/layout";
import { usePaneChromeStore } from "../stores/paneChrome";
import type { PaneChromeAction, PaneChromeControl } from "../stores/paneChrome";
import PluginPaneHost from "./PluginPaneHost.vue";
import { dockProviders } from "./registries";

const props = defineProps<{ pane: FloatingPane }>();
const layout = useLayoutStore();
const paneChrome = usePaneChromeStore();

const root = ref<HTMLElement | null>(null);

const provider = computed(() =>
  dockProviders.find((entry) => entry.type === props.pane.content.paneType),
);
const dockInstance = computed(() =>
  provider.value?.instances.find((entry) => entry.id === props.pane.content.instanceId),
);
const automaticTitle = computed(() => {
  const base = provider.value?.title ?? props.pane.content.paneType;
  return props.pane.content.instanceId === "main"
    ? base
    : `${base} #${props.pane.content.instanceId}`;
});
const icon = computed(() => dockInstance.value?.icon ?? provider.value?.icon ?? "bi-puzzle");
const tooltip = computed(() => dockInstance.value?.label ?? title.value);
const uid = computed(() => contentUid(props.pane.content));
const chrome = computed(() => paneChrome.forUid(uid.value));
const title = computed(() => chrome.value?.title ?? automaticTitle.value);

function runAction(action: PaneChromeAction): void {
  void action.run();
}

function updateControl(control: PaneChromeControl, event: Event): void {
  if (control.kind !== "select") return;
  const target = event.target as HTMLSelectElement | null;
  if (target === null) return;
  void control.onChange(target.value);
}

/** Workspace bounds, for clamping drag/resize so the pane stays reachable. */
function workspaceRect(): DOMRect | null {
  return root.value?.closest(".workspace")?.getBoundingClientRect() ?? null;
}

function startDrag(event: PointerEvent): void {
  if ((event.target as HTMLElement).closest("button, select, input, a") !== null) return;
  const bounds = workspaceRect();
  if (bounds === null) return;
  event.preventDefault();
  const pointerId = event.pointerId;
  (event.currentTarget as HTMLElement).setPointerCapture(pointerId);
  const startX = event.clientX - props.pane.x;
  const startY = event.clientY - props.pane.y;

  const move = (moveEvent: PointerEvent) => {
    const maxX = bounds.width - 48;
    const maxY = bounds.height - 32;
    const x = Math.min(maxX, Math.max(-props.pane.w + 64, moveEvent.clientX - startX));
    const y = Math.min(maxY, Math.max(0, moveEvent.clientY - startY));
    layout.moveFloating(props.pane.id, x, y);
  };
  const stop = () => {
    window.removeEventListener("pointermove", move);
    window.removeEventListener("pointerup", stop);
    window.removeEventListener("pointercancel", stop);
  };
  window.addEventListener("pointermove", move);
  window.addEventListener("pointerup", stop);
  window.addEventListener("pointercancel", stop);
}

function startResize(event: PointerEvent): void {
  const bounds = workspaceRect();
  if (bounds === null) return;
  event.preventDefault();
  const pointerId = event.pointerId;
  (event.currentTarget as HTMLElement).setPointerCapture(pointerId);
  const startX = event.clientX;
  const startY = event.clientY;
  const startW = props.pane.w;
  const startH = props.pane.h;

  const move = (moveEvent: PointerEvent) => {
    const w = Math.min(bounds.width - props.pane.x, startW + moveEvent.clientX - startX);
    const h = Math.min(bounds.height - props.pane.y, startH + moveEvent.clientY - startY);
    layout.resizeFloating(props.pane.id, Math.max(FLOAT_MIN_W, w), Math.max(FLOAT_MIN_H, h));
  };
  const stop = () => {
    window.removeEventListener("pointermove", move);
    window.removeEventListener("pointerup", stop);
    window.removeEventListener("pointercancel", stop);
  };
  window.addEventListener("pointermove", move);
  window.addEventListener("pointerup", stop);
  window.addEventListener("pointercancel", stop);
}
</script>

<template>
  <div
    ref="root"
    class="floating-pane"
    :style="{
      left: `${pane.x}px`,
      top: `${pane.y}px`,
      width: `${pane.w}px`,
      height: `${pane.h}px`,
      zIndex: pane.z,
    }"
    @pointerdown="layout.raiseFloating(pane.id)"
  >
    <header class="pane-titlebar" @pointerdown="startDrag">
      <i class="bi pane-icon" :class="icon" :title="tooltip"></i>
      <span class="pane-title" :title="tooltip">{{ title }}</span>
      <span v-if="chrome?.status" class="pane-status" :class="chrome.statusClass">
        {{ chrome.status }}
      </span>
      <span class="pane-actions">
        <button
          v-for="action in chrome?.actions ?? []"
          :key="action.id"
          type="button"
          class="pane-btn pane-custom-action"
          :class="{
            active: action.active,
            danger: action.variant === 'danger',
            'has-label': action.icon === undefined,
          }"
          :title="action.title"
          :aria-label="action.title"
          @click.stop="runAction(action)"
        >
          <i v-if="action.icon" class="bi" :class="action.icon"></i>
          <span v-else>{{ action.label }}</span>
        </button>
        <template v-for="control in chrome?.controls ?? []" :key="control.id">
          <select
            v-if="control.kind === 'select'"
            class="pane-control-select"
            :class="{ compact: control.size === 'compact' }"
            :title="control.title"
            :value="control.value"
            @pointerdown.stop
            @change="updateControl(control, $event)"
          >
            <option v-for="option in control.options" :key="option" :value="option">
              {{ option }}
            </option>
          </select>
        </template>
        <button
          type="button"
          class="pane-btn"
          title="刷新"
          @click.stop="layout.refreshFloating(pane.id)"
        >
          <i class="bi bi-arrow-clockwise"></i>
        </button>
        <button
          type="button"
          class="pane-btn"
          title="放回布局"
          @click.stop="layout.dockFloating(pane.id)"
        >
          <i class="bi bi-window-dock"></i>
        </button>
        <button
          type="button"
          class="pane-btn"
          title="关闭面板"
          @click.stop="layout.closeFloating(pane.id)"
        >
          <i class="bi bi-x-lg"></i>
        </button>
      </span>
    </header>
    <div class="floating-body">
      <PluginPaneHost
        :key="`${uid}:${pane.epoch}`"
        :pane-type="pane.content.paneType"
        :instance-id="pane.content.instanceId"
      />
    </div>
    <div class="floating-resize" title="拖动调整大小" @pointerdown.stop="startResize"></div>
  </div>
</template>

<style scoped>
.floating-pane {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  box-shadow: 0 8px 28px rgb(0 0 0 / 35%);
  display: flex;
  flex-direction: column;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
  pointer-events: auto;
  position: absolute;
}

.pane-titlebar {
  align-items: center;
  background: var(--color-titlebar);
  border-bottom: 1px solid var(--color-border);
  color: var(--color-titlebar-text);
  cursor: grab;
  display: flex;
  flex: 0 0 var(--pane-titlebar-height);
  font-size: var(--font-size-ui-small);
  gap: 6px;
  min-height: 0;
  padding: 0 4px 0 8px;
  touch-action: none;
  user-select: none;
}

.pane-titlebar:active {
  cursor: grabbing;
}

.pane-icon {
  font-size: var(--nav-icon-size);
}

.pane-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pane-status {
  color: var(--color-text-subtle);
  flex: 0 1 auto;
  font-size: 10px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pane-actions {
  align-items: center;
  display: flex;
  gap: 2px;
  margin-left: auto;
}

.pane-btn {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  display: inline-flex;
  font-size: var(--nav-icon-size);
  height: var(--nav-button-size);
  justify-content: center;
  padding: 0;
  width: var(--nav-button-size);
}

.pane-btn:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.pane-btn.active {
  color: var(--color-text);
}

.pane-btn.danger {
  color: var(--color-danger);
}

.pane-btn.has-label {
  font-size: 10px;
  font-weight: 700;
  min-width: max-content;
  padding: 0 6px;
  white-space: nowrap;
  width: auto;
}

.pane-control-select {
  background-color: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-text);
  flex: 0 0 120px;
  font-size: var(--font-size-ui-small);
  height: var(--nav-button-size);
  min-width: 80px;
  padding: 0 18px 0 5px;
}

.pane-control-select.compact {
  flex-basis: 64px;
  min-width: 54px;
}

.floating-body {
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
}

.floating-resize {
  bottom: 0;
  cursor: nwse-resize;
  height: 14px;
  position: absolute;
  right: 0;
  touch-action: none;
  width: 14px;
}

.floating-resize::after {
  border-bottom: 2px solid var(--color-text-subtle);
  border-right: 2px solid var(--color-text-subtle);
  bottom: 3px;
  content: "";
  height: 7px;
  position: absolute;
  right: 3px;
  width: 7px;
}
</style>
