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
import { registerComponent, registerDockActions, registerDockProvider, unregisterComponent, unregisterDockActions, unregisterDockProvider } from "./registries";

/** Plugin ids the static stage-A build already registered. */
const staticIds = new Set<string>();

/** Live external plugins: id → loaded module handle. */
const externalPlugins = new Map<string, { hash: string; module: PluginModule; ctx: PluginCtx; dockType?: string; actionIds?: string[]; styleEl?: HTMLLinkElement }>();

/** Failed import attempts: id → {hash, at}; retried after a cooldown. */
const failedImports = new Map<string, { hash: string; at: number }>();
const IMPORT_RETRY_MS = 30_000;

/** Register one plugin module's contributions (shared by both stages). */
function registerModule(plugin: PluginModule, ctx: PluginCtx): { dockType?: string; actionIds?: string[] } {
  const result: { dockType?: string; actionIds?: string[] } = {};
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
    Promise.resolve(plugin.activate(ctx)).catch((error: unknown) => {
      console.error(`plugin ${plugin.id} activate failed`, error);
    });
  }
  return result;
}

function unregisterModule(handle: { module: PluginModule; ctx: PluginCtx; dockType?: string; actionIds?: string[]; styleEl?: HTMLLinkElement }): void {
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
    registerModule(plugin, createCtx(plugin.id));
  }
}

/** Assets mailbox entry shape (gateway assets.go assetEntry). */
interface AssetsMailboxEntry {
  url: string;
  entry: string;
  hash: string;
  files?: string[];
  manifest?: { name?: string; icon?: string; description?: string };
}

/** Registry mailbox entry shape (kernel broker RegistryEntry). */
interface RegistryMailboxEntry {
  id?: string;
  manifest?: { id?: string };
}

/** Latest mailbox snapshots; reconcileExternal derives the live set from
 *  their intersection. */
let latestAssets: Record<string, AssetsMailboxEntry> = {};
let connectedIds = new Set<string>();

/** Reconciles are serialized: assets and list frames can arrive together
 *  and import() is async, so naive overlap could register one plugin twice. */
let reconcileChain: Promise<void> = Promise.resolve();

function scheduleReconcile(): void {
  reconcileChain = reconcileChain.then(reconcileExternal).catch((error: unknown) => {
    console.error("external plugin reconcile failed", error);
  });
}

/** Subscribe the assets and registry mailboxes and reconcile external plugin
 *  loads. Called once at bootstrap; mailbox redelivery on reconnect
 *  re-reconciles, which is a no-op for already-loaded hashes. */
export function loadExternalPlugins(): void {
  bus.subscribe("plugins:_:assets", (frame) => {
    latestAssets = (frame.value as Record<string, AssetsMailboxEntry> | null) ?? {};
    scheduleReconcile();
  });
  bus.subscribe("plugins:_:list", (frame) => {
    const entries = Array.isArray(frame.value) ? (frame.value as RegistryMailboxEntry[]) : [];
    connectedIds = new Set(
      entries
        .map((entry) => entry?.id ?? entry?.manifest?.id)
        .filter((id): id is string => typeof id === "string" && id !== ""),
    );
    scheduleReconcile();
  });
}

async function reconcileExternal(): Promise<void> {
  // Unload: ids whose assets disappeared (manager delete / assets:remove)
  // or whose backend disconnected (no entry point without a live backend).
  for (const id of [...externalPlugins.keys()]) {
    if (!(id in latestAssets) || !connectedIds.has(id)) {
      const handle = externalPlugins.get(id);
      externalPlugins.delete(id);
      if (handle !== undefined) {
        unregisterModule(handle);
        console.info(`external plugin ${id} unloaded`);
      }
    }
  }
  for (const [id, entry] of Object.entries(latestAssets)) {
    if (staticIds.has(id)) continue; // in-repo build wins; never double-register
    if (!connectedIds.has(id)) continue; // offline: assets are not an entry point
    const loaded = externalPlugins.get(id);
    if (loaded !== undefined && loaded.hash === entry.hash) continue;
    const failed = failedImports.get(id);
    if (loaded === undefined && failed !== undefined && failed.hash === entry.hash && Date.now() - failed.at < IMPORT_RETRY_MS) {
      continue; // same broken bundle: wait out the cooldown instead of import-looping
    }
    if (loaded !== undefined) {
      // Hot reload (framework 8.6 rule 2): the content hash changed, so the
      // URL differs and import() bypasses the module cache.
      unregisterModule(loaded);
      externalPlugins.delete(id);
    }
    await importExternal(id, entry);
  }
}

async function importExternal(id: string, entry: AssetsMailboxEntry): Promise<void> {
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
    const { dockType, actionIds } = registerModule(plugin, ctx);
    externalPlugins.set(id, { hash: entry.hash, module: plugin, ctx, dockType, actionIds, styleEl });
    failedImports.delete(id);
    console.info(`external plugin ${id} loaded`, url);
  } catch (error) {
    styleEl?.remove();
    failedImports.set(id, { hash: entry.hash, at: Date.now() });
    console.error(`external plugin ${id} failed to load from ${url}`, error);
  }
}
