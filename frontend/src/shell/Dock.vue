<script setup lang="ts">
/**
 * The Dock (framework section 8.5) lists only provider-contributed persistent
 * or open items. "+" opens tools and creates instances; the bottom gear opens
 * the unified settings pane (`settings:main`).
 */
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

import { useDockSettingsStore, clampDockWidth } from "../stores/dockSettings";
import { useLayoutStore } from "../stores/layout";
import type { DockItemAction, DockProvider } from "./definePlugin";
import { collectDockSections, openDockEntry, openProvider, type DockEntry, type DockSection } from "./dockModel";
import { openShellInstance } from "./instances";
import { dockActions, dockProviders } from "./registries";

const layout = useLayoutStore();
const dockSettings = useDockSettingsStore();
const menuOpen = ref(false);
const expanded = ref(false);
const navEl = ref<HTMLElement | null>(null);
// Pinned: the Dock stays expanded and its width takes real layout space
// (compressing the panes), instead of overlaying them on hover.
const pinned = computed(() => dockSettings.pinned);
const isExpanded = computed(() => pinned.value || expanded.value);
const hoverExpandMs = computed(() => dockSettings.hoverExpandMs);
// Width: drag override while resizing, otherwise the persisted setting.
const dragWidth = ref<number | null>(null);
const dockWidth = computed(() => dragWidth.value ?? dockSettings.width);

function startResize(event: PointerEvent): void {
  event.preventDefault();
  const pointerId = event.pointerId;
  (event.currentTarget as HTMLElement).setPointerCapture(pointerId);
  const startX = event.clientX;
  const startWidth = dockWidth.value;

  const move = (moveEvent: PointerEvent): void => {
    dragWidth.value = clampDockWidth(startWidth + moveEvent.clientX - startX);
  };

  const stop = (upEvent: PointerEvent): void => {
    window.removeEventListener("pointermove", move);
    window.removeEventListener("pointerup", stop);
    window.removeEventListener("pointercancel", stop);
    dockSettings.setWidth(startWidth + upEvent.clientX - startX);
    dragWidth.value = null;
    // Overlay mode: pointerleave was suppressed during the drag, so if the
    // pointer ended up outside the Dock, collapse it now.
    if (!pinned.value && navEl.value !== null) {
      const rect = navEl.value.getBoundingClientRect();
      // The expanded panel spans [rect.left, rect.left + dockWidth].
      const inside =
        upEvent.clientX >= rect.left && upEvent.clientX <= rect.left + dockWidth.value &&
        upEvent.clientY >= rect.top && upEvent.clientY <= rect.bottom;
      if (!inside) expanded.value = false;
    }
  };

  window.addEventListener("pointermove", move);
  window.addEventListener("pointerup", stop);
  window.addEventListener("pointercancel", stop);
}
let expandTimer: ReturnType<typeof setTimeout> | null = null;

function clearExpandTimer(): void {
  if (expandTimer === null) return;
  clearTimeout(expandTimer);
  expandTimer = null;
}

function handlePointerEnter(event: PointerEvent): void {
  if (event.pointerType === "touch" || isExpanded.value || expandTimer !== null) return;
  if (hoverExpandMs.value === 0) {
    expanded.value = true;
    return;
  }
  expandTimer = setTimeout(() => {
    expandTimer = null;
    expanded.value = true;
  }, hoverExpandMs.value);
}

function handlePointerLeave(): void {
  clearExpandTimer();
  // Never collapse mid-resize: the drag handle sits at the expanded panel's
  // right edge, so a rightward drag immediately leaves the hover region.
  if (!pinned.value && dragWidth.value === null) expanded.value = false;
}

function toggleDockPin(): void {
  dockSettings.setPinned(!pinned.value);
}

function closePopovers(): void {
  menuOpen.value = false;
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") closePopovers();
}

onMounted(() => {
  window.addEventListener("keydown", handleKeydown);
  void pollSchedRestart();
  schedRestartTimer = setInterval(pollSchedRestart, 15000);
});
onBeforeUnmount(() => {
  clearExpandTimer();
  window.removeEventListener("keydown", handleKeydown);
  if (schedRestartTimer !== null) clearInterval(schedRestartTimer);
});

const sections = computed<DockSection[]>(() => collectDockSections(dockProviders));
// Group expansion is a browser-local preference, independent of hover expansion.
function isGroupCollapsed(key: string): boolean {
  return !dockSettings.expandedGroups.includes(key);
}

function toggleGroup(key: string): void {
  dockSettings.setGroupExpanded(key, isGroupCollapsed(key));
}

/** Status-dot class for an instance state word; unknown words fall back to the neutral dot. */
function dockDotClass(state: string): string {
  if (state === "running" || state === "error" || state === "unread") return state;
  return "dead";
}

const activeUid = computed(() => {
  const content = layout.activePane.content;
  return content === null ? null : `${content.paneType}:${content.instanceId}`;
});

const creatable = computed(() => dockProviders);

function openEntry(entry: DockEntry): void {
  void openDockEntry(entry, { open: openShellInstance });
}

function runEntryAction(action: DockItemAction): void {
  void action.run();
}

async function createFrom(provider: DockProvider): Promise<void> {
  menuOpen.value = false;
  await openProvider(provider, { open: openShellInstance });
}

async function createFromGroup(section: DockSection): Promise<void> {
  if (section.group === undefined) return;
  dockSettings.setGroupExpanded(section.key, true);
  await openProvider(section.provider, { open: openShellInstance }, section.group.id);
}

function openSettings(): void {
  menuOpen.value = false;
  void openShellInstance({ paneType: "settings", instanceId: "main" });
}

/**
 * Scheduled-restart indicator (framework v0.44): a deferred build+restart
 * can be armed from Settings or by an agent over the gateway HTTP API
 * (POST /api/admin/schedule-restart). Poll the status endpoint lightly so a
 * pending restart is visible without opening Settings; clicking the
 * indicator opens Settings where it can be cancelled.
 */
type SchedRestartStatus = "none" | "building" | "armed" | "failed";
const schedRestart = ref<SchedRestartStatus>("none");
let schedRestartTimer: ReturnType<typeof setInterval> | null = null;

async function pollSchedRestart(): Promise<void> {
  try {
    const resp = await fetch("/api/admin/schedule-restart", { cache: "no-store" });
    if (!resp.ok) return;
    const body = (await resp.json()) as { status?: string };
    schedRestart.value =
      body.status === "building" || body.status === "armed" || body.status === "failed" ? body.status : "none";
  } catch {
    // Gateway down (the scheduled restart may have just fired) — keep last state.
  }
}

const schedRestartTitle = computed<string>(() => {
  switch (schedRestart.value) {
    case "building":
      return "已计划重启：后台构建中（点击打开设置，可取消）";
    case "armed":
      return "已计划重启：等系统空闲后自动重启（点击打开设置，可取消）";
    case "failed":
      return "计划重启构建失败：保持现状（点击打开设置）";
    default:
      return "";
  }
});

// Frontend build stamp (vite define), shown in the Dock foot when expanded:
// "YYYY-MM-DD HH:mm" in local time.
const buildTime = (() => {
  const date = new Date(__BUILD_TIME__);
  const pad = (value: number): string => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
})();
</script>

<template>
  <nav
    ref="navEl"
    class="dock"
    :class="{ expanded: isExpanded, pinned, resizing: dragWidth !== null }"
    :style="{ '--dock-expanded-width': `${dockWidth}px` }"
    @pointerenter="handlePointerEnter"
    @pointerleave="handlePointerLeave"
  >
    <div class="dock-inner">
      <div class="dock-plus-wrap">
        <button
          type="button"
          class="dock-btn dock-plus"
          title="新建 instance"
          @click="menuOpen = !menuOpen"
        >
          <i class="bi bi-plus-lg"></i>
        </button>
        <div v-if="menuOpen" class="dock-menu-overlay" @click="menuOpen = false"></div>
        <div v-if="menuOpen" class="dock-menu">
          <button
            v-for="provider in creatable"
            :key="provider.type"
            type="button"
            class="dock-menu-item"
            @click="createFrom(provider)"
          >
            <i class="bi" :class="provider.icon"></i>
            <span>{{ provider.title }}</span>
          </button>
          <div v-if="creatable.length === 0" class="dock-menu-empty">没有可用的插件</div>
        </div>
      </div>

      <div class="dock-sep"></div>

      <div class="dock-items">
        <template v-for="section in sections" :key="section.key">
          <div v-if="section.group !== undefined && isExpanded" class="dock-group-header">
            <button
              type="button"
              class="dock-group-toggle"
              :title="`${isGroupCollapsed(section.key) ? '展开' : '折叠'} ${section.group.label}`"
              :aria-expanded="!isGroupCollapsed(section.key)"
              @click="toggleGroup(section.key)"
            >
              <i class="bi" :class="section.provider.icon"></i>
              <span class="dock-group-label">{{ section.group.label }}</span>
              <span v-if="section.group.meta !== undefined" class="dock-group-meta">
                <span
                  v-for="(item, index) in section.group.meta()"
                  :key="index"
                  class="dock-group-meta-item"
                  :class="{ 'dock-group-meta-success': item.tone === 'success' }"
                  :title="item.title"
                  :aria-label="item.title"
                >{{ item.text }}</span>
              </span>
              <span v-else class="dock-group-count">{{ section.entries.length }}</span>
            </button>
            <button
              v-if="section.provider.createForGroup !== undefined"
              type="button"
              class="dock-group-add"
              :title="`在 ${section.group.label} 下新建 Task`"
              :aria-label="`在 ${section.group.label} 下新建 Task`"
              @click="createFromGroup(section)"
            >
              <i class="bi bi-plus-lg"></i>
            </button>
          </div>
          <div
            v-for="entry in section.entries"
            v-show="!isExpanded || section.group === undefined || !isGroupCollapsed(section.key)"
            :key="entry.key"
            class="dock-item"
            :class="{ active: entry.uid === activeUid, 'dock-grouped-item': isExpanded && section.group !== undefined }"
          >
            <button type="button" class="dock-btn" :title="entry.label" :aria-label="entry.label" @click="openEntry(entry)">
              <i class="bi" :class="entry.icon"></i>
              <span v-if="isExpanded" class="dock-label">{{ entry.displayLabel }}</span>
            </button>
            <span v-if="entry.instance?.meta?.()" class="dock-item-meta">{{ entry.instance.meta() }}</span>
            <button
              v-for="action in entry.instance?.actions ?? []"
              v-show="isExpanded"
              :key="action.id"
              type="button"
              class="dock-item-action"
              :class="{ active: action.active === true, danger: action.variant === 'danger' }"
              :title="action.title"
              :aria-label="action.title"
              @click.stop="runEntryAction(action)"
            >
              <i v-if="action.icon" class="bi" :class="action.icon"></i>
              <span v-if="action.label">{{ action.label }}</span>
            </button>
            <span v-if="entry.state !== undefined" class="dock-dot" :class="dockDotClass(entry.state)"></span>
          </div>
        </template>
        <div v-if="sections.length === 0" class="dock-empty" title="没有正在运行的 instance"><i class="bi bi-dash-lg"></i></div>
      </div>

      <div class="dock-foot">
        <button
          v-if="schedRestart !== 'none'"
          type="button"
          class="dock-btn dock-sched"
          :class="`dock-sched-${schedRestart}`"
          :title="schedRestartTitle"
          :aria-label="schedRestartTitle"
          @click="openSettings"
        ><i class="bi" :class="schedRestart === 'failed' ? 'bi-exclamation-triangle' : 'bi-clock-history'"></i></button>
        <button
          v-for="action in dockActions"
          :key="action.id"
          type="button"
          class="dock-btn dock-action"
          :class="{ 'dock-action-active': action.active?.() === true }"
          :title="action.title()"
          :aria-label="action.title()"
          :aria-pressed="action.active?.() === true"
          @click="action.onClick()"
        ><i class="bi" :class="action.icon()"></i><span v-if="action.badge?.()" class="dock-action-badge">{{ action.badge?.() }}</span></button>
        <button
          type="button"
          class="dock-btn"
          :class="{ 'dock-pin-active': pinned }"
          :title="pinned ? '取消固定侧边栏' : '固定侧边栏（保持展开）'"
          :aria-label="pinned ? '取消固定侧边栏' : '固定侧边栏（保持展开）'"
          @click="toggleDockPin"
        ><i class="bi" :class="pinned ? 'bi-pin-angle-fill' : 'bi-pin-angle'"></i></button>
        <button type="button" class="dock-btn" title="设置" aria-label="设置" @click="openSettings"><i class="bi bi-gear"></i></button>
        <span v-if="isExpanded" class="dock-build" :title="`前端构建时间：${buildTime}`">{{ buildTime }}</span>
      </div>
      <div
        v-if="isExpanded"
        class="dock-resizer"
        role="separator"
        title="拖动调整侧边栏宽度"
        @pointerdown="startResize"
      ></div>
    </div>
  </nav>
</template>

<style scoped>
.dock {
  flex: 0 0 var(--dock-width);
  min-height: 0;
  position: relative;
  transition: flex-basis 160ms ease;
  width: var(--dock-width);
}

/* Pinned: the expanded width takes real layout space, compressing the
   panes to the right instead of overlaying them. Flat style: no floating
   shadow in either mode. */
.dock.pinned {
  flex-basis: var(--dock-expanded-width);
  width: var(--dock-expanded-width);
}

/* No width animation while dragging the resizer. */
.dock.resizing,
.dock.resizing .dock-inner {
  transition: none;
}

.dock-resizer {
  bottom: 0;
  cursor: col-resize;
  position: absolute;
  right: -3px;
  top: 0;
  width: 7px;
  z-index: 45;
}

.dock-resizer:hover {
  background: var(--color-accent);
  opacity: 0.35;
  right: -1px;
  width: 3px;
}

.dock-inner {
  align-items: flex-start;
  background: var(--color-surface);
  border-right: 1px solid var(--color-border);
  display: flex;
  flex-direction: column;
  height: 100%;
  left: 0;
  overflow: visible;
  /* Left inset pins the icon column to its collapsed-center position, so
     icons never move while the width transition runs (29px = 28px button
     + 1px border-right, border-box). */
  padding: 4px 0 4px calc((var(--dock-width) - 29px) / 2);
  position: absolute;
  top: 0;
  transition: width 160ms ease;
  width: var(--dock-width);
}

.dock.expanded .dock-inner {
  z-index: 40;
  width: var(--dock-expanded-width);
}

.dock-plus-wrap {
  position: relative;
}

.dock-btn {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: var(--radius-md);
  color: var(--color-text-muted);
  display: inline-flex;
  font-size: calc(var(--font-size-ui) * 7 / 6);
  height: 28px;
  justify-content: center;
  padding: 0;
  width: 28px;
}

.dock-btn:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

/* Scheduled-restart indicator (framework v0.44): warning tint while a
   deferred restart is pending, danger tint after a failed build. */
.dock-sched-building,
.dock-sched-armed {
  color: var(--color-warning);
}

.dock-sched-failed {
  color: var(--color-danger);
}

.dock-plus {
  border: 1px dashed var(--color-border-strong);
}

.dock-sep {
  background: var(--color-border);
  flex: 0 0 1px;
  margin: 4px 0 4px 4px;
  width: 20px;
}

.dock-items {
  align-items: flex-start;
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 0;
  min-height: 0;
  overflow-x: hidden;
  overflow-y: auto;
  width: 100%;
}

.dock-group-header {
  align-items: center;
  display: flex;
  flex: 0 0 auto;
  gap: 2px;
  min-height: 22px;
  width: calc(100% - 6px);
}

.dock-group-toggle,
.dock-group-add {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  display: inline-flex;
  font-size: calc(var(--font-size-ui) * 7 / 6);
  height: 22px;
  padding: 0;
}

.dock-group-toggle {
  flex: 1 1 auto;
  gap: 8px;
  justify-content: flex-start;
  min-width: 0;
  padding-left: 9px;
}

.dock-group-toggle:hover,
.dock-group-add:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.dock-group-label {
  font-size: var(--font-size-ui);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dock-group-count,
.dock-group-meta {
  flex: 0 0 auto;
  font-size: var(--font-size-ui-small);
  margin-left: auto;
  padding-right: 3px;
  white-space: nowrap;
}

.dock-group-count {
  color: var(--color-text-subtle);
}

.dock-group-meta {
  align-items: center;
  color: var(--color-text-muted);
  display: inline-flex;
  gap: 6px;
}

.dock-group-meta-success {
  color: var(--color-success);
}

.dock-group-add {
  flex: 0 0 20px;
  font-size: var(--font-size-ui-small);
  justify-content: center;
  width: 20px;
}

.dock.expanded .dock-item.dock-grouped-item {
  margin-left: 10px;
  width: calc(100% - 16px);
}

.dock.expanded .dock-grouped-item > .dock-btn {
  padding-left: 6px;
}

.dock-item {
  align-items: center;
  display: flex;
  min-width: 0;
  position: relative;
  width: 28px;
}

.dock-items .dock-btn {
  height: 22px;
}

.dock-item-action {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  display: inline-flex;
  flex: 0 0 auto;
  font-size: var(--font-size-ui-small);
  gap: 3px;
  height: 20px;
  justify-content: center;
  min-width: 20px;
  padding: 0 3px;
}
.dock-item-action:hover { background: var(--color-surface-hover); color: var(--color-text); }
.dock-item-action.active { color: var(--color-accent); }
.dock-item-action.danger:hover { color: var(--color-danger); }
.dock-item-meta {
  background: var(--color-surface-selected);
  border-radius: 8px;
  color: var(--color-accent);
  font-size: 9px;
  line-height: 14px;
  min-width: 14px;
  padding: 0 3px;
  position: absolute;
  right: -2px;
  text-align: center;
  top: -3px;
  z-index: 2;
}

.dock.expanded .dock-item {
  width: calc(100% - 6px);
}

.dock.expanded .dock-item > .dock-btn {
  flex: 1 1 auto;
  gap: 8px;
  justify-content: flex-start;
  min-width: 0;
  padding: 0 9px;
  width: auto;
}
.dock.expanded .dock-item-meta {
  flex: 0 0 auto;
  font-size: var(--font-size-ui-small);
  line-height: 18px;
  margin-right: 3px;
  position: static;
}

.dock-label {
  font-size: var(--font-size-ui);
  min-width: 0;
  overflow: hidden;
  text-align: left;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dock-item.active .dock-btn {
  background: var(--color-surface-selected);
  color: var(--color-accent);
}

.dock-dot {
  background: var(--color-text-subtle);
  border-radius: 50%;
  bottom: 1px;
  height: 6px;
  position: absolute;
  right: 1px;
  width: 6px;
}

.dock-dot.running {
  background: var(--color-success);
}

.dock-dot.unread {
  background: var(--color-warning);
}

.dock-dot.error {
  background: var(--color-danger);
}

.dock-dot.dead {
  background: var(--color-text-subtle);
}

.dock.expanded .dock-dot {
  left: 24px;
  right: auto;
}

.dock-empty {
  color: var(--color-text-subtle);
  margin-top: 2px;
}

.dock-pin-active {
  color: var(--color-accent);
}

/* Global dock actions (e.g. voice control): accent tint while active. */
.dock-action { position: relative; }
.dock-action-badge { align-items: center; background: var(--color-danger); border-radius: 8px; color: var(--color-surface); display: flex; font-size: 9px; height: 14px; justify-content: center; min-width: 14px; padding: 0 3px; position: absolute; right: -2px; top: -3px; }
.dock-action-active {
  color: var(--color-danger);
}

.dock-menu-overlay {
  inset: 0;
  position: fixed;
  z-index: 30;
}

.dock-menu {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  display: flex;
  flex-direction: column;
  left: calc(100% + 6px);
  min-width: 150px;
  padding: 3px;
  position: absolute;
  top: 0;
  z-index: 31;
}

.dock-menu-item {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text);
  display: flex;
  font-size: var(--font-size-ui);
  gap: 8px;
  padding: 6px 8px;
  text-align: left;
  white-space: nowrap;
}

.dock-menu-item:hover {
  background: var(--color-surface-hover);
}

.dock-menu-empty {
  color: var(--color-text-subtle);
  font-size: var(--font-size-ui-small);
  padding: 6px 8px;
}

.dock-foot {
  flex: 0 0 auto;
  padding-top: 2px;
  position: relative;
}

.dock-build {
  color: var(--color-text-subtle);
  display: block;
  font-size: calc(var(--font-size-ui) * 5 / 6);
  overflow: hidden;
  padding: 2px 8px 4px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dock-menu-item:disabled {
  color: var(--color-text-subtle);
  cursor: default;
}

.dock-menu-item:disabled:hover {
  background: transparent;
}
</style>
