/**
 * F1/F2: shell registries — one set for static (in-repo) and future dynamic
 * (external bundle) registration alike (framework section 8.6).
 */

import { markRaw, reactive, type Component } from "vue";

import type { DockAction, DockProvider, PluginInstanceAdapter } from "./definePlugin";

export interface PluginDescriptor {
  id: string;
  name: string;
  builtin: boolean;
}

/** Plugins with a loaded frontend module; built-ins are always enabled. */
export const pluginCatalog = reactive(new Map<string, PluginDescriptor>());

/** Plugin-owned settings components rendered by the unified plugin manager. */
export const settingsRegistry = reactive(new Map<string, Component>());

/** instance.type → component. */
export const componentRegistry = reactive(new Map<string, Component>());

/** instance.type → plugin-owned create/reopen adapter. */
export const instanceRegistry = reactive(new Map<string, PluginInstanceAdapter>());

/** Dock providers in registration order. */
export const dockProviders = reactive<DockProvider[]>([]);

/** Global Dock-foot action buttons in registration order. */
export const dockActions = reactive<DockAction[]>([]);

export function registerComponent(type: string, component: Component): void {
  componentRegistry.set(type, markRaw(component));
}

export function unregisterComponent(type: string): void {
  componentRegistry.delete(type);
}

export function registerInstanceAdapter(adapter: PluginInstanceAdapter): void {
  instanceRegistry.set(adapter.type, adapter);
}

export function unregisterInstanceAdapter(type: string): void {
  instanceRegistry.delete(type);
}

export function registerPlugin(id: string, name: string, builtin: boolean, settings?: Component): void {
  pluginCatalog.set(id, { id, name: name || id, builtin });
  if (settings !== undefined) settingsRegistry.set(id, markRaw(settings));
  else settingsRegistry.delete(id);
}

export function unregisterPlugin(id: string): void {
  pluginCatalog.delete(id);
  settingsRegistry.delete(id);
}

export function registerDockProvider(provider: DockProvider): void {
  if (!dockProviders.some((entry) => entry.type === provider.type)) {
    dockProviders.push(provider);
  }
}

export function unregisterDockProvider(type: string): void {
  const index = dockProviders.findIndex((entry) => entry.type === type);
  if (index >= 0) dockProviders.splice(index, 1);
}

export function registerDockActions(actions: DockAction[]): void {
  for (const action of actions) {
    if (!dockActions.some((entry) => entry.id === action.id)) dockActions.push(action);
  }
}

export function unregisterDockActions(ids: string[]): void {
  for (const id of ids) {
    const index = dockActions.findIndex((entry) => entry.id === id);
    if (index >= 0) dockActions.splice(index, 1);
  }
}
