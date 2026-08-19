/**
 * Plugin discovery and loading (framework section 8.4).
 *
 * Stage A (in-repo): plugin modules at `src/plugins/<id>/index.ts`,
 * discovered via `import.meta.glob` at bootstrap; per-plugin code-splitting
 * keeps index modules tiny (component SFCs load via defineAsyncComponent).
 *
 * Stage B (external): the gateway's `plugins:_:assets` mailbox maps plugin
 * id → content-addressed bundle URL. `loadExternalPlugins` subscribes to it
 * and reconciles: new ids get `import(url)`ed, changed hashes reload
 * (deactivate old → import new), removed ids deactivate and unregister
 * (logical unload — framework 8.6 rule 3: module bytes stay in memory,
 * panes fall back to the unknown-type placeholder until reload).
 */

import { createCtx, type PluginCtx } from "./ctx";
import { bus } from "./bus";
import type { PluginModule } from "./definePlugin";
import { registerComponent, registerDockProvider, unregisterComponent, unregisterDockProvider } from "./registries";

/** Plugin ids the static stage-A build already registered. */
const staticIds = new Set<string>();

/** Live external plugins: id → loaded module handle. */
const externalPlugins = new Map<string, { hash: string; module: PluginModule; ctx: PluginCtx; dockType?: string; styleEl?: HTMLLinkElement }>();

/** Failed import attempts: id → {hash, at}; retried after a cooldown. */
const failedImports = new Map<string, { hash: string; at: number }>();
const IMPORT_RETRY_MS = 30_000;

/** Register one plugin module's contributions (shared by both stages). */
function registerModule(plugin: PluginModule, ctx: PluginCtx): string | undefined {
  let dockType: string | undefined;
  for (const [type, component] of Object.entries(plugin.components ?? {})) {
    registerComponent(type, component);
  }
  if (plugin.createDockProvider !== undefined) {
    try {
      const provider = plugin.createDockProvider(ctx);
      dockType = provider.type;
      registerDockProvider(provider);
    } catch (error) {
      console.error(`plugin ${plugin.id} createDockProvider failed`, error);
    }
  }
  if (plugin.activate !== undefined) {
    Promise.resolve(plugin.activate(ctx)).catch((error: unknown) => {
      console.error(`plugin ${plugin.id} activate failed`, error);
    });
  }
  return dockType;
}

function unregisterModule(handle: { module: PluginModule; ctx: PluginCtx; dockType?: string; styleEl?: HTMLLinkElement }): void {
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

/** Subscribe the assets mailbox and reconcile external plugin loads. Called
 *  once at bootstrap; mailbox redelivery on reconnect re-reconciles, which
 *  is a no-op for already-loaded hashes. */
export function loadExternalPlugins(): void {
  bus.subscribe("plugins:_:assets", (frame) => {
    const value = frame.value as Record<string, AssetsMailboxEntry> | null;
    void reconcileExternal(value ?? {});
  });
}

async function reconcileExternal(assets: Record<string, AssetsMailboxEntry>): Promise<void> {
  // Unload: ids whose assets disappeared (manager delete / assets:remove).
  for (const id of [...externalPlugins.keys()]) {
    if (!(id in assets)) {
      const handle = externalPlugins.get(id);
      externalPlugins.delete(id);
      if (handle !== undefined) {
        unregisterModule(handle);
        console.info(`external plugin ${id} unloaded`);
      }
    }
  }
  for (const [id, entry] of Object.entries(assets)) {
    if (staticIds.has(id)) continue; // in-repo build wins; never double-register
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
    const dockType = registerModule(plugin, ctx);
    externalPlugins.set(id, { hash: entry.hash, module: plugin, ctx, dockType, styleEl });
    failedImports.delete(id);
    console.info(`external plugin ${id} loaded`, url);
  } catch (error) {
    styleEl?.remove();
    failedImports.set(id, { hash: entry.hash, at: Date.now() });
    console.error(`external plugin ${id} failed to load from ${url}`, error);
  }
}
