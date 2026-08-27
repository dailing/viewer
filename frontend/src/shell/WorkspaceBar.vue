<script setup lang="ts">
import { computed } from "vue";

import { contentUid, type LayoutMode } from "../stores/layout";
import { useLayoutStore } from "../stores/layout";
import { usePaneChromeStore } from "../stores/paneChrome";
import type { PaneChromeAction, PaneChromeControl } from "../stores/paneChrome";
import { dockProviders } from "./registries";

const layout = useLayoutStore();
const paneChrome = usePaneChromeStore();
const pane = computed(() => layout.activePane);
const content = computed(() => pane.value.content);
const provider = computed(() =>
  content.value === null
    ? undefined
    : dockProviders.find((entry) => entry.type === content.value?.paneType),
);
const instance = computed(() =>
  content.value === null
    ? undefined
    : provider.value?.instances.find((entry) => entry.id === content.value?.instanceId),
);
const uid = computed(() => (content.value === null ? "" : contentUid(content.value)));
const chrome = computed(() => paneChrome.forUid(uid.value));
const automaticTitle = computed(() => {
  if (content.value === null) return "空面板";
  const base = provider.value?.title ?? content.value.paneType;
  return content.value.instanceId === "main" ? base : `${base} #${content.value.instanceId}`;
});
const title = computed(() => chrome.value?.title ?? automaticTitle.value);
const tooltip = computed(() => instance.value?.label ?? title.value);
const icon = computed(() => instance.value?.icon ?? provider.value?.icon ?? "bi-puzzle");
const paneIndex = computed(() => layout.panes.findIndex((entry) => entry.id === pane.value.id));

const modes: Array<{ value: LayoutMode; label: string }> = [
  { value: "cascade", label: "自适应主次" },
  { value: "columns", label: "横向均分" },
  { value: "rows", label: "纵向均分" },
  { value: "free", label: "自由分割" },
];

function runAction(action: PaneChromeAction): void {
  void action.run();
}

function updateControl(control: PaneChromeControl, event: Event): void {
  if (control.kind !== "select") return;
  const target = event.target as HTMLSelectElement | null;
  if (target !== null) void control.onChange(target.value);
}
</script>

<template>
  <header class="workspace-bar">
    <i v-if="content" class="bi workspace-icon" :class="icon" :title="tooltip"></i>
    <span class="workspace-title" :class="{ empty: !content }" :title="tooltip">{{ title }}</span>
    <span v-if="chrome?.status" class="workspace-status" :class="chrome.statusClass">
      {{ chrome.status }}
    </span>
    <span class="workspace-actions">
      <button
        v-for="action in chrome?.actions ?? []"
        :key="action.id"
        type="button"
        class="workspace-btn"
        :class="{ active: action.active, danger: action.variant === 'danger', labeled: !action.icon }"
        :title="action.title"
        @click="runAction(action)"
      >
        <i v-if="action.icon" class="bi" :class="action.icon"></i>
        <span v-else>{{ action.label }}</span>
      </button>
      <template v-for="control in chrome?.controls ?? []" :key="control.id">
        <select
          v-if="control.kind === 'select'"
          class="workspace-select chrome-select"
          :class="{ compact: control.size === 'compact' }"
          :title="control.title"
          :value="control.value"
          @change="updateControl(control, $event)"
        >
          <option v-for="option in control.options" :key="option" :value="option">{{ option }}</option>
        </select>
        <span v-else class="workspace-chips" :title="control.title">
          <span v-for="(item, index) in control.items" :key="`${index}:${item}`">{{ item }}</span>
        </span>
      </template>
      <span class="workspace-divider"></span>
      <select
        class="workspace-select mode-select"
        title="平铺布局"
        :value="layout.mode"
        @change="layout.setMode(($event.target as HTMLSelectElement).value as LayoutMode)"
      >
        <option v-for="mode in modes" :key="mode.value" :value="mode.value">{{ mode.label }}</option>
      </select>
      <button class="workspace-btn" title="向树根移动一格" :disabled="paneIndex <= 0" @click="layout.movePane(pane.id, -1)">
        <i class="bi bi-arrow-bar-left"></i>
      </button>
      <button class="workspace-btn" title="向叶端移动一格" :disabled="paneIndex >= layout.panes.length - 1" @click="layout.movePane(pane.id, 1)">
        <i class="bi bi-arrow-bar-right"></i>
      </button>
      <button v-if="content" class="workspace-btn" title="刷新" @click="layout.refreshPane(pane.id)">
        <i class="bi bi-arrow-clockwise"></i>
      </button>
      <button v-if="layout.mode !== 'free'" class="workspace-btn" title="新增面板" @click="layout.addPane()">
        <i class="bi bi-plus-lg"></i>
      </button>
      <template v-else>
        <button class="workspace-btn" title="向右分隔" @click="layout.splitPane(pane.id, 'vertical')">
          <i class="bi bi-layout-split"></i>
        </button>
        <button class="workspace-btn" title="向下分隔" @click="layout.splitPane(pane.id, 'horizontal')">
          <i class="bi bi-layout-split rotate-90"></i>
        </button>
      </template>
      <button v-if="content" class="workspace-btn" title="浮动面板" @click="layout.floatPane(pane.id)">
        <i class="bi bi-window-stack"></i>
      </button>
      <button class="workspace-btn" title="关闭面板" @click="layout.closePane(pane.id)">
        <i class="bi bi-x-lg"></i>
      </button>
    </span>
  </header>
</template>

<style scoped>
.workspace-bar {
  align-items: center;
  background: var(--color-titlebar);
  border-bottom: 1px solid var(--color-border);
  color: var(--color-titlebar-text);
  display: flex;
  flex: 0 0 var(--pane-titlebar-height);
  font-size: var(--font-size-ui-small);
  gap: 6px;
  min-width: 0;
  padding: 0 4px 0 8px;
}

.workspace-icon { font-size: var(--nav-icon-size); }
.workspace-title { color: var(--color-text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.workspace-title.empty, .workspace-status { color: var(--color-text-subtle); }
.workspace-status { flex: 0 1 auto; font-size: 10px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.workspace-actions { align-items: center; display: flex; gap: 2px; margin-left: auto; min-width: 0; }
.workspace-btn {
  align-items: center; background: transparent; border: 0; border-radius: var(--radius-sm); color: var(--color-text-muted);
  display: inline-flex; font-size: var(--nav-icon-size); height: var(--nav-button-size); justify-content: center; padding: 0; width: var(--nav-button-size);
}
.workspace-btn:hover:not(:disabled) { background: var(--color-surface-hover); color: var(--color-text); }
.workspace-btn:disabled { opacity: .3; }
.workspace-btn.active { color: var(--color-text); }
.workspace-btn.danger { color: var(--color-danger); }
.workspace-btn.labeled { font-size: 10px; font-weight: 700; min-width: max-content; padding: 0 6px; width: auto; }
.workspace-select { background: var(--color-surface); border: 1px solid var(--color-border); border-radius: var(--radius-sm); color: var(--color-text); font-size: var(--font-size-ui-small); height: var(--nav-button-size); }
.mode-select { max-width: 104px; }
.chrome-select { max-width: 120px; min-width: 54px; }
.chrome-select.compact { max-width: 68px; }
.workspace-chips { display: flex; gap: 2px; max-width: 180px; overflow: hidden; }
.workspace-chips span { background: var(--color-surface); color: var(--color-text-muted); font-size: 10px; overflow: hidden; padding: 2px 5px; text-overflow: ellipsis; white-space: nowrap; }
.workspace-divider { border-left: 1px solid var(--color-border); height: 16px; margin: 0 2px; }
.rotate-90 { transform: rotate(90deg); }

@media (max-width: 720px) {
  .workspace-title, .workspace-status, .workspace-divider { display: none; }
  .workspace-actions { overflow-x: auto; }
}
</style>
