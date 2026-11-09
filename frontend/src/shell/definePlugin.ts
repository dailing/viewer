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

export type PluginCardinality = "singleton" | "multi";

/** Structured input to the single plugin-facing instance opening path. */
export interface InstanceOpenRequest<P = unknown> {
  paneType: string;
  /** Existing instance to restore/update; omitted means create from params. */
  instanceId?: string;
  /** JSON-serializable plugin-specific opening parameters. */
  params?: P;
  /** Force a fresh pane placement, not necessarily a fresh instance. */
  newPane?: boolean;
}

export interface OpenedInstance {
  paneId: string;
  instanceId: string;
}

/** Plugin-owned factory/reopen logic; Shell owns subsequent pane placement. */
export interface PluginInstanceAdapter {
  type: string;
  cardinality: PluginCardinality;
  open(request: InstanceOpenRequest): string | Promise<string>;
}

export interface DockInstanceGroup {
  id: string;
  label: string;
}

/** One running instance shown in the shell Dock. */
export interface DockInstance {
  id: string;
  /** Tooltip text, e.g. "#3 /usr/bin/zsh · /home/d". */
  label: string;
  /** Optional state word ("running" / "unread" / "error" / …) — drives the status dot color. */
  state?: string;
  /** Per-instance icon override; falls back to the provider's icon. */
  icon?: string;
  /** Optional weak grouping header; the instance label remains the primary text. */
  group?: DockInstanceGroup;
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
  /** Reactive list of running instances, maintained by the provider. */
  instances: DockInstance[];
  /** "+" menu action; returning an instance id opens its pane. */
  create?: () => Promise<string | void> | string | void;
  /** Group-row action; creates an instance inside the specified weak group. */
  createForGroup?: (groupId: string) => Promise<string | void> | string | void;
  /** Remove an instance (Dock hover ×); absent = not removable. */
  remove?: (instanceId: string) => Promise<void> | void;
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
  /** Optional compact numeric/text badge. */
  badge?: () => string | number;
  /** Optional highlighted state (e.g. listening). */
  active?: () => boolean;
  onClick: () => void;
}

export interface PluginModule {
  id: string;
  /** Display name used by the unified plugin manager. */
  name?: string;
  /** instance.type → component (lazy via defineAsyncComponent). */
  components?: Record<string, Component>;
  /** Optional plugin-owned settings surface rendered inside 插件管理. */
  settings?: Component;
  /** Build instance factories/restorers for this plugin's pane types. */
  createInstanceAdapters?: (ctx: PluginCtx) => PluginInstanceAdapter[];
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
