/**
 * F1/F2: shell registries — one set for static (in-repo) and future dynamic
 * (external bundle) registration alike (framework section 8.6).
 */

import { markRaw, reactive, type Component } from "vue";

import type { SidebarTool } from "./definePlugin";

/** instance.type → component. */
export const componentRegistry = reactive(new Map<string, Component>());

/** Sidebar tools in registration order. */
export const sidebarTools = reactive<SidebarTool[]>([]);

export function registerComponent(type: string, component: Component): void {
  componentRegistry.set(type, markRaw(component));
}

export function unregisterComponent(type: string): void {
  componentRegistry.delete(type);
}

export function registerSidebarTool(tool: SidebarTool): void {
  if (!sidebarTools.some((entry) => entry.id === tool.id)) {
    sidebarTools.push(tool);
  }
}

export function unregisterSidebarTool(id: string): void {
  const index = sidebarTools.findIndex((entry) => entry.id === id);
  if (index >= 0) sidebarTools.splice(index, 1);
}
