<script setup lang="ts">
/**
 * The Dock (framework section 8.5): every running instance across all dock
 * providers, macOS-style. "+" creates a new instance; the bottom plug shows
 * the bus connection state. There is deliberately no other chrome — the old
 * top NavBar is gone.
 */
import { computed, ref } from "vue";

import { useLayoutStore } from "../stores/layout";
import { busState } from "./bus";
import type { DockInstance, DockProvider } from "./definePlugin";
import { dockProviders } from "./registries";

const layout = useLayoutStore();
const menuOpen = ref(false);

interface DockEntry {
  key: string;
  uid: string;
  icon: string;
  label: string;
  state?: string;
  provider: DockProvider;
  instance?: DockInstance;
}

const entries = computed<DockEntry[]>(() => {
  const result: DockEntry[] = [];
  for (const provider of dockProviders) {
    if (provider.singleton === true) {
      const uid = `${provider.type}:main`;
      if (layout.isUidOpen(uid)) {
        result.push({
          key: uid,
          uid,
          icon: provider.icon,
          label: provider.title,
          provider,
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

const plugClass = computed(() => (busState.connected ? "bi-plug-fill ok" : "bi-plug bad"));
const plugTip = computed(() =>
  busState.connected ? `已连接${busState.conn !== null ? ` · ${busState.conn}` : ""}` : "连接中…",
);

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
</script>

<template>
  <nav class="dock">
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
        <button type="button" class="dock-btn" :title="entry.label" @click="openEntry(entry)">
          <i class="bi" :class="entry.icon"></i>
        </button>
        <span
          v-if="entry.state !== undefined"
          class="dock-dot"
          :class="entry.state === 'running' ? 'running' : 'dead'"
        ></span>
        <button
          v-if="entry.provider.remove !== undefined && entry.instance !== undefined"
          type="button"
          class="dock-remove"
          title="终止"
          @click="removeEntry(entry)"
        >
          <i class="bi bi-x"></i>
        </button>
      </div>
      <div v-if="entries.length === 0" class="dock-empty" title="没有正在运行的 instance">
        <i class="bi bi-dash-lg"></i>
      </div>
    </div>

    <div class="dock-foot">
      <i class="bi" :class="plugClass" :title="plugTip"></i>
    </div>
  </nav>
</template>

<style scoped>
.dock {
  align-items: center;
  background: var(--color-surface);
  border-right: 1px solid var(--color-border);
  display: flex;
  flex: 0 0 var(--dock-width);
  flex-direction: column;
  min-height: 0;
  padding: 6px 0;
  width: var(--dock-width);
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
  margin: 6px 0;
  width: 24px;
}

.dock-items {
  align-items: center;
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
  position: relative;
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

.dock-remove {
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

.dock-item:hover .dock-remove {
  display: inline-flex;
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
}

.dock-foot .bi {
  font-size: 14px;
}

.dock-foot .ok {
  color: var(--color-success);
}

.dock-foot .bad {
  color: var(--color-danger);
}
</style>
