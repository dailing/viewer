/**
 * Frontend plugin SDK (framework sections 8.2/8.3/8.6/14.2): the contract an
 * external plugin's built `frontend.js` bundle must satisfy. The bundle's
 * default export is a PluginModule created with definePlugin; the shell
 * dynamic-imports it from the gateway's asset store and injects ctx.
 *
 * These types structurally mirror the shell's (shell/definePlugin.ts,
 * shell/ctx.ts, stores/paneChrome.ts) — the shell duck-types the module, so
 * a plugin built against this SDK stays compatible without sharing code.
 * Components are Vue SFCs compiled by the plugin's own build; typed as
 * `PluginComponent` (unknown) here to avoid a vue type dependency.
 */

import type { FrameHandler } from "./client.js";

/** Structural stand-in for a Vue component (avoid depending on vue types). */
export type PluginComponent = unknown;

/**
 * The narrowed bus surface the shell injects (shell/ctx.ts). Subscriptions
 * made through ctx.bus are tracked and auto-unsubscribed on dispose —
 * plugins never write cleanup code (framework 8.3 rule 6).
 */
export interface CtxBus {
  subscribe(pattern: string, handler: FrameHandler): void;
  unsubscribe(pattern: string, handler: FrameHandler): void;
  publish(channel: string, value: unknown, options?: { traceId?: string; depth?: number }): Promise<void>;
  set(channel: string, value: unknown, options?: { traceId?: string; depth?: number }): Promise<void>;
  request<T = unknown>(channel: string, value?: unknown, options?: { timeout?: number; traceId?: string; depth?: number }): Promise<T>;
  cancel(channel: string, corr: string, options?: { depth?: number }): Promise<void>;
}

/** F6: synchronous namespaced localStorage (`viewer:<scope>:` prefix). */
export interface CtxStorage {
  get<T>(key: string, fallback: T): T;
  set(key: string, value: unknown): void;
  remove(key: string): void;
}

/**
 * The ctx injected into activate()/createDockProvider() (plugin scope) and
 * into pane components via inject("pluginCtx") (pane scope).
 */
export interface PluginCtx {
  /** Plugin or pane scope, e.g. `my-plugin` or `my-plugin:instance-1`. */
  readonly scope: string;
  /** Instance segment of the scope (`"_"` for plugin-level activation). */
  readonly instanceId: string;
  readonly bus: CtxBus;
  readonly storage: CtxStorage;
  /** Register this scope's shell-rendered title-bar content. */
  setChrome(chrome: PaneChrome): void;
  /** Register a cleanup run on dispose (pane closed, plugin deactivated). */
  onDispose(fn: () => void): void;
  dispose(): void;
}

/** Alias for readability at component injection sites. */
export type PluginPaneCtx = PluginCtx;

export interface PaneChromeAction {
  id: string;
  title: string;
  icon?: string;
  label?: string;
  active?: boolean;
  variant?: "default" | "danger";
  run: () => void | Promise<void>;
}

export type PaneChromeControl =
  | { kind: "select"; id: string; title: string; value: string; options: string[]; size?: "compact"; onChange: (value: string) => void | Promise<void> }
  | { kind: "chips"; id: string; title?: string; items: string[] };

export interface PaneChrome {
  title?: string;
  status?: string;
  statusClass?: string;
  actions?: PaneChromeAction[];
  controls?: PaneChromeControl[];
}

/** One running instance shown in the shell Dock (section 8.7). */
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
 * A plugin's contribution to the Dock: the live list of its running
 * instances plus how to create a new one (section 8.5).
 */
export interface DockProvider {
  /** instance.type — the paneType used when opening one of these instances. */
  type: string;
  icon: string;
  /** "+" menu entry label. */
  title: string;
  /** Singleton providers have no backend-tracked instances; the Dock pins
   *  their entry by default and lets users persistently unpin it. */
  singleton?: boolean;
  /** Reactive list of running instances, maintained by the provider. */
  instances: DockInstance[];
  /** "+" menu action (create a new instance); absent = not user-creatable. */
  create?: () => Promise<void> | void;
  /** Dock hover action to remove a running instance (e.g. kill a terminal). */
  remove?: (id: string) => Promise<void> | void;
}

/**
 * A plugin's contribution to the Dock foot: a global action button next to
 * pin/settings. Getters are called during render, so ref-backed state stays
 * reactive.
 */
export interface DockAction {
  id: string;
  icon: () => string;
  title: () => string;
  /** Optional highlighted state (e.g. listening). */
  active?: () => boolean;
  onClick: () => void;
}

/** The bundle's default-exported module shape. */
export interface PluginModule {
  id: string;
  /** instance.type → component (lazy via defineAsyncComponent). Components
   *  receive the pane ctx via inject("pluginCtx"). */
  components?: Record<string, PluginComponent>;
  /** Build the Dock contribution; called once at load with plugin ctx. */
  createDockProvider?: (ctx: PluginCtx) => DockProvider;
  /** Build global Dock-foot action buttons; called once at load. */
  createDockActions?: (ctx: PluginCtx) => DockAction[];
  /** Called once at load with the plugin-scope ctx. */
  activate?: (ctx: PluginCtx) => void | Promise<void>;
  /** Called on hot unload/reload, before registries are cleared. */
  deactivate?: () => void;
}

/** Identity helper for typing the default export. */
export function definePlugin(module: PluginModule): PluginModule {
  return module;
}
