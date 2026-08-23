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

/** One running instance shown in the shell Dock. */
export interface DockInstance {
  id: string;
  /** Tooltip text, e.g. "#3 /usr/bin/zsh · /home/d". */
  label: string;
  /** Optional state word ("running" / "unread" / "error" / …) — drives the status dot color. */
  state?: string;
  /** Per-instance icon override; falls back to the provider's icon. */
  icon?: string;
}

/**
 * A plugin's contribution to the Dock: the live list of its running instances
 * plus how to create a new one (framework section 8.5).
 */
export interface DockProvider {
  /** instance.type — the paneType used when opening one of these instances. */
  type: string;
  icon: string;
  /** "+" menu entry label. */
  title: string;
  /**
   * Singleton providers (bus-inspector) have no backend-tracked instances;
   * their entry is always listed in the Dock. A singleton provider may still
   * carry `instances` (files): the Dock lists them below its root entry.
   */
  singleton?: boolean;
  /**
   * Singleton root entry acts as a launcher: clicking it calls `create()`
   * instead of opening `type:main` (files — the root entry never opens).
   */
  clickCreates?: boolean;
  /** Reactive list of running instances, maintained by the provider. */
  instances: DockInstance[];
  /** "+" menu action (create a new instance); absent = not user-creatable. */
  create?: () => Promise<void> | void;
}

/**
 * A plugin's contribution to the Dock foot: a global action button next to
 * pin/settings (framework section 8.5). Getters are called during render, so
 * ref-backed state stays reactive.
 */
export interface DockAction {
  id: string;
  icon: () => string;
  title: () => string;
  /** Optional highlighted state (e.g. listening). */
  active?: () => boolean;
  onClick: () => void;
}

export interface PluginModule {
  id: string;
  /** instance.type → component (lazy via defineAsyncComponent). */
  components?: Record<string, Component>;
  /** Build the Dock contribution; called once at bootstrap with plugin ctx. */
  createDockProvider?: (ctx: PluginCtx) => DockProvider;
  /** Build global Dock-foot action buttons; called once at bootstrap. */
  createDockActions?: (ctx: PluginCtx) => DockAction[];
  /** Called once at bootstrap with the plugin-scope ctx. */
  activate?: (ctx: PluginCtx) => void | Promise<void>;
  deactivate?: () => void;
}

export function definePlugin(module: PluginModule): PluginModule {
  return module;
}
