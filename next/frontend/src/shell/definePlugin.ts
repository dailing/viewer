/**
 * Plugin module contract (framework sections 8.2/14.2).
 *
 * Stage A (in-repo): each plugin lives at `src/plugins/<id>/index.ts` and
 * default-exports `definePlugin({...})`; the shell discovers them via
 * `import.meta.glob` and registers their contributions into the shell
 * registries. Stage B (external bundles) keeps the same `activate(ctx)` entry.
 */

import type { Component } from "vue";

import type { PluginCtx } from "./ctx";

/** A sidebar entry; clicking it opens a pane of `paneType`. */
export interface SidebarTool {
  id: string;
  icon: string;
  title: string;
  paneType: string;
}

export interface PluginModule {
  id: string;
  /** instance.type → component (lazy via defineAsyncComponent). */
  components?: Record<string, Component>;
  sidebarTools?: SidebarTool[];
  /** Called once at bootstrap with the plugin-scope ctx. */
  activate?: (ctx: PluginCtx) => void | Promise<void>;
  deactivate?: () => void;
}

export function definePlugin(module: PluginModule): PluginModule {
  return module;
}
