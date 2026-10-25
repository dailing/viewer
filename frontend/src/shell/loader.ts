/**
 * Plugin discovery and loading (framework section 8.4).
 *
 * Stage A (in-repo): plugin modules at `src/plugins/<id>/index.ts`,
 * discovered via `import.meta.glob` at bootstrap; per-plugin code-splitting
 * keeps index modules tiny (component SFCs load via defineAsyncComponent).
 *
 * Stage B (external): the gateway's `plugins:_:assets` mailbox maps plugin
 * id → content-addressed bundle URL, and the kernel's `plugins:_:list`
 * mailbox carries the live connection registry. `loadExternalPlugins`
 * subscribes to both and reconciles: an entry point exists only for ids in
 * the intersection (bundle available AND backend connected) — assets alone
 * must never resurrect the dock icon of a dead plugin. New ids get
 * `import(url)`ed, changed hashes reload (deactivate old → import new),
 * removed/disconnected ids deactivate and unregister (logical unload —
 * framework 8.6 rule 3: module bytes stay in memory, panes fall back to the
 * unknown-type placeholder until reload).
 */

import { createCtx, type PluginCtx } from "./ctx";
import { bus } from "./bus";
import type { PluginModule } from "./definePlugin";
import { ExternalPluginLoader, type ExternalAssetEntry } from "./externalLoader";
import { registerComponent, registerDockActions, registerDockProvider, unregisterComponent, unregisterDockActions, unregisterDockProvider } from "./registries";

/** Plugin ids the static stage-A build already registered. */
const staticIds = new Set<string>();

interface ExternalHandle {
  module: PluginModule;
  ctx: PluginCtx;
  dockType?: string;
  actionIds?: string[];
  styleEl?: HTMLLinkElement;
}

interface ModuleRegistration {
  dockType?: string;
  actionIds?: string[];
  activation?: Promise<void>;
}

/** Register one plugin module's contributions (shared by both stages). */
function registerModule(plugin: PluginModule, ctx: PluginCtx): ModuleRegistration {
  const result: ModuleRegistration = {};
  for (const [type, component] of Object.entries(plugin.components ?? {})) {
    registerComponent(type, component);
  }
  if (plugin.createDockProvider !== undefined) {
    try {
      const provider = plugin.createDockProvider(ctx);
      result.dockType = provider.type;
      registerDockProvider(provider);
    } catch (error) {
      console.error(`plugin ${plugin.id} createDockProvider failed`, error);
    }
  }
  if (plugin.createDockActions !== undefined) {
    try {
      const actions = plugin.createDockActions(ctx);
      result.actionIds = actions.map((action) => action.id);
      registerDockActions(actions);
    } catch (error) {
      console.error(`plugin ${plugin.id} createDockActions failed`, error);
    }
  }
  if (plugin.activate !== undefined) {
    result.activation = Promise.resolve(plugin.activate(ctx));
  }
  return result;
}

function reportActivationError(plugin: PluginModule, activation: Promise<void> | undefined): void {
  activation?.catch((error: unknown) => {
    console.error(`plugin ${plugin.id} activate failed`, error);
  });
}

function unregisterModule(handle: ExternalHandle): void {
  try {
    handle.module.deactivate?.();
  } catch (error) {
    console.error(`plugin ${handle.module.id} deactivate failed`, error);
  }
  handle.ctx.dispose();
  for (const type of Object.keys(handle.module.components ?? {})) {
    unregisterComponent(type);
  }
  if (handle.dockType !== undefined) unregisterDockProvider(handle.dockType);
  if (handle.actionIds !== undefined) unregisterDockActions(handle.actionIds);
  handle.styleEl?.remove();
}

export function loadPlugins(): void {
  const modules = import.meta.glob<{ default: PluginModule }>("../plugins/*/index.ts", {
    eager: true,
  });
  for (const [path, module] of Object.entries(modules)) {
    const plugin = module.default;
    if (plugin === undefined) {
      console.warn(`plugin module ${path} has no default export`);
      continue;
    }
    staticIds.add(plugin.id);
    const { activation } = registerModule(plugin, createCtx(plugin.id));
    reportActivationError(plugin, activation);
  }
}

/** Registry mailbox entry shape (kernel broker RegistryEntry). */
interface RegistryMailboxEntry {
  id?: string;
  manifest?: { id?: string };
}

const externalLoader = new ExternalPluginLoader<ExternalHandle>({
  isStatic: (id) => staticIds.has(id),
  load: importExternal,
  unload: (handle) => {
    unregisterModule(handle);
    console.info(`external plugin ${handle.module.id} unloaded`);
  },
  onError: (id, entry, error) => {
    console.error(`external plugin ${id} failed to load from ${entry.url + entry.entry}`, error);
  },
});

/** Subscribe the assets and registry mailboxes and reconcile external plugin
 * loads. Called once at bootstrap; mailbox redelivery on reconnect
 * re-reconciles, which is a no-op for already-loaded hashes. */
export function loadExternalPlugins(): void {
  bus.subscribe("plugins:_:assets", (frame) => {
    externalLoader.setAssets((frame.value as Record<string, ExternalAssetEntry> | null) ?? {});
  });
  bus.subscribe("plugins:_:list", (frame) => {
    const entries = Array.isArray(frame.value) ? (frame.value as RegistryMailboxEntry[]) : [];
    externalLoader.setConnected(
      new Set(
        entries
          .map((entry) => entry?.id ?? entry?.manifest?.id)
          .filter((id): id is string => typeof id === "string" && id !== ""),
      ),
    );
  });
}

async function importExternal(id: string, entry: ExternalAssetEntry): Promise<ExternalHandle> {
  const url = entry.url + entry.entry;
  // Optional companion stylesheet: a Vite library build with
  // cssCodeSplit:false emits frontend.css next to the bundle.
  let styleEl: HTMLLinkElement | undefined;
  if (entry.files?.includes("frontend.css") === true) {
    styleEl = document.createElement("link");
    styleEl.rel = "stylesheet";
    styleEl.href = entry.url + "frontend.css";
    styleEl.dataset["pluginAssets"] = id;
    document.head.appendChild(styleEl);
  }
  try {
    const module = (await import(/* @vite-ignore */ url)) as { default?: PluginModule };
    const plugin = module.default;
    if (plugin === undefined || plugin.id !== id) {
      throw new Error(`bundle ${url} has no definePlugin default export with id "${id}"`);
    }
    const ctx = createCtx(id);
    const { dockType, actionIds, activation } = registerModule(plugin, ctx);
    const handle: ExternalHandle = { module: plugin, ctx, dockType, actionIds, styleEl };
    try {
      await activation;
    } catch (error) {
      // Activation is part of loading: roll back every partial contribution
      // so the same hash can be retried instead of remaining half-registered.
      unregisterModule(handle);
      throw error;
    }
    console.info(`external plugin ${id} loaded`, url);
    return handle;
  } catch (error) {
    styleEl?.remove();
    throw error;
  }
}
