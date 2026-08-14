<script setup lang="ts">
/**
 * Pane container: a title bar (instance identity + refresh/split/close
 * actions) hosting one instance's component. Clicking anywhere focuses the
 * pane — new Dock opens land in the focused pane.
 */
import { computed } from "vue";

import { contentUid, type PaneNode } from "../stores/layout";
import { useLayoutStore } from "../stores/layout";
import { usePaneChromeStore } from "../stores/paneChrome";
import type { PaneChromeAction, PaneChromeControl } from "../stores/paneChrome";
import PluginPaneHost from "./PluginPaneHost.vue";
import { dockProviders } from "./registries";

const props = defineProps<{ pane: PaneNode }>();
const layout = useLayoutStore();
const paneChrome = usePaneChromeStore();

const provider = computed(() =>
  props.pane.content === null
    ? undefined
    : dockProviders.find((entry) => entry.type === props.pane.content?.paneType),
);

const dockInstance = computed(() =>
  props.pane.content === null
    ? undefined
    : provider.value?.instances.find((entry) => entry.id === props.pane.content?.instanceId),
);

const automaticTitle = computed(() => {
  if (props.pane.content === null) return "";
  const base = provider.value?.title ?? props.pane.content.paneType;
  return props.pane.content.instanceId === "main"
    ? base
    : `${base} #${props.pane.content.instanceId}`;
});

const icon = computed(() => dockInstance.value?.icon ?? provider.value?.icon ?? "bi-puzzle");
const tooltip = computed(() => dockInstance.value?.label ?? title.value);
const isActive = computed(() => layout.activePaneId === props.pane.id);
const uid = computed(() => (props.pane.content === null ? "" : contentUid(props.pane.content)));
const chrome = computed(() => paneChrome.forUid(uid.value));
const title = computed(() => chrome.value?.title ?? automaticTitle.value);

function runAction(action: PaneChromeAction): void {
  layout.setActivePane(props.pane.id);
  void action.run();
}

function updateControl(control: PaneChromeControl, event: Event): void {
  if (control.kind !== "select") return;
  const target = event.target as HTMLSelectElement | null;
  if (target === null) return;
  layout.setActivePane(props.pane.id);
  void control.onChange(target.value);
}
</script>

<template>
  <div
    class="pane-container"
    :class="{ active: isActive }"
    @pointerdown="layout.setActivePane(pane.id)"
  >
    <header class="pane-titlebar">
      <template v-if="pane.content !== null">
        <i class="bi pane-icon" :class="icon" :title="tooltip"></i>
        <span class="pane-title" :title="tooltip">{{ title }}</span>
        <span v-if="chrome?.status" class="pane-status" :class="chrome.statusClass">
          {{ chrome.status }}
        </span>
      </template>
      <span v-else class="pane-title pane-title-empty">空面板</span>
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
          <span v-else class="pane-control-chips" :title="control.title">
            <span
              v-for="(item, index) in control.items"
              :key="`${index}:${item}`"
              class="pane-control-chip"
            >
              {{ item }}
            </span>
          </span>
        </template>
        <button
          v-if="pane.content !== null"
          type="button"
          class="pane-btn"
          title="刷新"
          @click.stop="layout.refreshPane(pane.id)"
        >
          <i class="bi bi-arrow-clockwise"></i>
        </button>
        <button
          type="button"
          class="pane-btn"
          title="向右分隔"
          @click.stop="layout.splitPane(pane.id, 'vertical')"
        >
          <i class="bi bi-layout-split"></i>
        </button>
        <button
          type="button"
          class="pane-btn"
          title="向下分隔"
          @click.stop="layout.splitPane(pane.id, 'horizontal')"
        >
          <i class="bi bi-layout-split rotate-90"></i>
        </button>
        <button
          type="button"
          class="pane-btn"
          title="关闭面板"
          @click.stop="layout.closePane(pane.id)"
        >
          <i class="bi bi-x-lg"></i>
        </button>
      </span>
    </header>
    <div class="pane-body">
      <PluginPaneHost
        v-if="pane.content !== null"
        :key="`${uid}:${pane.epoch}`"
        :pane-type="pane.content.paneType"
        :instance-id="pane.content.instanceId"
      />
      <div v-else class="pane-placeholder">
        <i class="bi bi-plus-square-dotted"></i>
        <div>从左侧 Dock 打开一个 instance，或点 + 新建</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pane-container {
  background: var(--color-surface);
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
  width: 100%;
}

.pane-titlebar {
  align-items: center;
  background: var(--color-surface-muted);
  border-bottom: 1px solid var(--color-border);
  color: var(--color-text-muted);
  display: flex;
  flex: 0 0 var(--pane-titlebar-height);
  font-size: var(--font-size-ui-small);
  gap: 6px;
  min-height: 0;
  padding: 0 4px 0 8px;
}

.pane-container.active .pane-titlebar {
  box-shadow: inset 0 -2px 0 var(--color-accent);
  color: var(--color-text);
}

.pane-icon {
  font-size: var(--nav-icon-size);
}

.pane-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pane-title-empty {
  color: var(--color-text-subtle);
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

.pane-control-chips {
  align-items: center;
  display: flex;
  gap: 2px;
  max-width: 180px;
  overflow: hidden;
}

.pane-control-chip {
  background: var(--color-surface);
  color: var(--color-text-muted);
  font-size: 10px;
  overflow: hidden;
  padding: 2px 5px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rotate-90 {
  transform: rotate(90deg);
}

.pane-body {
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
}

.pane-placeholder {
  align-items: center;
  color: var(--color-text-subtle);
  display: flex;
  flex-direction: column;
  font-size: var(--font-size-ui-small);
  gap: 6px;
  height: 100%;
  justify-content: center;
}

.pane-placeholder > i {
  font-size: 20px;
}
</style>
