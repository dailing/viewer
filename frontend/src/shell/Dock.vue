<script setup lang="ts">
/**
 * The Dock (framework section 8.5): every running instance across all dock
 * providers, macOS-style. "+" creates a new instance and the bottom gear
 * opens the unified settings pane (`settings:main`). There is deliberately
 * no other chrome.
 */
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

import { useDockSettingsStore } from "../stores/dockSettings";
import { useLayoutStore } from "../stores/layout";
import type { DockInstance, DockProvider } from "./definePlugin";
import { dockProviders } from "./registries";

const layout = useLayoutStore();
const dockSettings = useDockSettingsStore();
const menuOpen = ref(false);
const pinRevision = ref(0);
const expanded = ref(false);
const hoverExpandMs = computed(() => dockSettings.hoverExpandMs);
let expandTimer: ReturnType<typeof setTimeout> | null = null;

function clearExpandTimer(): void {
  if (expandTimer === null) return;
  clearTimeout(expandTimer);
  expandTimer = null;
}

function handlePointerEnter(event: PointerEvent): void {
  if (event.pointerType === "touch" || expanded.value || expandTimer !== null) return;
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
  expanded.value = false;
}

function closePopovers(): void {
  menuOpen.value = false;
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") closePopovers();
}

onMounted(() => window.addEventListener("keydown", handleKeydown));
onBeforeUnmount(() => {
  clearExpandTimer();
  window.removeEventListener("keydown", handleKeydown);
});

function pinStorageKey(type: string): string {
  return `viewer.dock.singletonPinned.v1.${type}`;
}

function isSingletonPinned(type: string): boolean {
  void pinRevision.value;
  return localStorage.getItem(pinStorageKey(type)) !== "false";
}

interface DockEntry {
  key: string;
  uid: string;
  icon: string;
  label: string;
  displayLabel: string;
  state?: string;
  provider: DockProvider;
  instance?: DockInstance;
  pinned?: boolean;
}

function chatDisplayLabel(label: string): string {
  const separator = " · ";
  const separatorIndex = label.lastIndexOf(separator);
  if (separatorIndex < 0) return label;

  const root = label.slice(separatorIndex + separator.length);
  const withoutTrailingSeparators = root.replace(/[\\/]+$/, "");
  const basename = withoutTrailingSeparators.split(/[\\/]/).pop() || root;
  return `${label.slice(0, separatorIndex)}${separator}${basename}`;
}

function instanceDisplayLabel(provider: DockProvider, instance: DockInstance): string {
  return provider.type === "chat" ? chatDisplayLabel(instance.label) : instance.label;
}

const entries = computed<DockEntry[]>(() => {
  const result: DockEntry[] = [];
  for (const provider of dockProviders) {
    if (provider.singleton === true) {
      const uid = `${provider.type}:main`;
      const pinned = isSingletonPinned(provider.type);
      if (pinned || layout.isUidOpen(uid)) {
        result.push({
          key: uid,
          uid,
          icon: provider.icon,
          label: provider.title,
          displayLabel: provider.title,
          provider,
          pinned,
        });
      }
      continue;
    }
    for (const instance of provider.instances) {
      const uid = `${provider.type}:${instance.id}`;
      result.push({
        key: uid,
        uid,
        icon: instance.icon ?? provider.icon,
        label: instance.label,
        displayLabel: instanceDisplayLabel(provider, instance),
        state: instance.state,
        provider,
        instance,
      });
    }
  }
  return result;
});

const activeUid = computed(() => {
  const content = layout.activePane.content;
  return content === null ? null : `${content.paneType}:${content.instanceId}`;
});

const creatable = computed(() => dockProviders);

function openEntry(entry: DockEntry): void {
  layout.openInstance(entry.provider.type, entry.instance?.id ?? "main");
}

async function createFrom(provider: DockProvider): Promise<void> {
  menuOpen.value = false;
  if (provider.singleton === true) {
    layout.openInstance(provider.type, "main");
    return;
  }
  await provider.create?.();
}

async function removeEntry(entry: DockEntry): Promise<void> {
  if (entry.instance === undefined) return;
  await entry.provider.remove?.(entry.instance.id);
}

function togglePin(entry: DockEntry): void {
  const pinned = entry.pinned !== false;
  localStorage.setItem(pinStorageKey(entry.provider.type), pinned ? "false" : "true");
  pinRevision.value += 1;
}

function openSettings(): void {
  menuOpen.value = false;
  layout.openInstance("settings", "main");
}
</script>

<template>
  <nav
    class="dock"
    :class="{ expanded }"
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
        <div
          v-for="entry in entries"
          :key="entry.key"
          class="dock-item"
          :class="{ active: entry.uid === activeUid }"
        >
          <button type="button" class="dock-btn" :title="entry.label" :aria-label="entry.label" @click="openEntry(entry)">
            <i class="bi" :class="entry.icon"></i>
            <span v-if="expanded" class="dock-label">{{ entry.displayLabel }}</span>
          </button>
          <span v-if="entry.state !== undefined" class="dock-dot" :class="entry.state === 'running' ? 'running' : 'dead'"></span>
          <button v-if="entry.provider.remove !== undefined && entry.instance !== undefined" type="button" class="dock-remove" title="终止" @click="removeEntry(entry)"><i class="bi bi-x"></i></button>
          <button v-if="entry.provider.singleton === true" type="button" class="dock-pin" :title="entry.pinned === false ? '固定到 Dock' : '从 Dock 取消固定'" @click="togglePin(entry)"><i class="bi" :class="entry.pinned === false ? 'bi-pin-angle' : 'bi-pin-angle-fill'"></i></button>
        </div>
        <div v-if="entries.length === 0" class="dock-empty" title="没有正在运行的 instance"><i class="bi bi-dash-lg"></i></div>
      </div>

      <div class="dock-foot">
        <button type="button" class="dock-btn" title="设置" aria-label="设置" @click="openSettings"><i class="bi bi-gear"></i></button>
      </div>
    </div>
  </nav>
</template>

<style scoped>
.dock {
  flex: 0 0 var(--dock-width);
  min-height: 0;
  position: relative;
  width: var(--dock-width);
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
     icons never move while the width transition runs (35px = 34px button
     + 1px border-right, border-box). */
  padding: 6px 0 6px calc((var(--dock-width) - 35px) / 2);
  position: absolute;
  top: 0;
  transition: width 160ms ease;
  width: var(--dock-width);
}

.dock.expanded .dock-inner {
  box-shadow: 4px 0 12px rgb(0 0 0 / 0.12);
  z-index: 40;
  width: 220px;
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
  font-size: 16px;
  height: 34px;
  justify-content: center;
  padding: 0;
  width: 34px;
}

.dock-btn:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.dock-plus {
  border: 1px dashed var(--color-border-strong);
}

.dock-sep {
  background: var(--color-border);
  flex: 0 0 1px;
  margin: 6px 0 6px 5px;
  width: 24px;
}

.dock-items {
  align-items: flex-start;
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 4px;
  min-height: 0;
  overflow-x: hidden;
  overflow-y: auto;
  width: 100%;
}

.dock-item {
  align-items: center;
  display: flex;
  min-width: 0;
  position: relative;
  width: 34px;
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

.dock-dot.dead {
  background: var(--color-text-subtle);
}

.dock.expanded .dock-dot {
  left: 27px;
  right: auto;
}

.dock-remove,
.dock-pin {
  align-items: center;
  background: var(--color-surface-raised);
  border: 1px solid var(--color-border);
  border-radius: 50%;
  color: var(--color-danger);
  display: none;
  font-size: 9px;
  height: 14px;
  justify-content: center;
  left: -2px;
  padding: 0;
  position: absolute;
  top: -2px;
  width: 14px;
}

.dock-pin {
  color: var(--color-text-muted);
}

.dock-item:hover .dock-remove,
.dock-item:hover .dock-pin {
  display: inline-flex;
}

.dock.expanded .dock-remove,
.dock.expanded .dock-pin {
  flex: 0 0 16px;
  left: auto;
  margin-right: 4px;
  position: static;
  top: auto;
}

.dock-empty {
  color: var(--color-text-subtle);
  margin-top: 4px;
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
  padding-top: 4px;
  position: relative;
}

.dock-menu-item:disabled {
  color: var(--color-text-subtle);
  cursor: default;
}

.dock-menu-item:disabled:hover {
  background: transparent;
}
</style>
