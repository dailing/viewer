/**
 * Stage-A plugin discovery (framework section 8.4): in-repo plugin modules at
 * `src/plugins/<id>/index.ts`, registered at bootstrap. Plugin index modules
 * stay tiny (component SFCs load via defineAsyncComponent), so eager
 * registration still code-splits per plugin.
 */

import { createCtx } from "./ctx";
import type { PluginModule } from "./definePlugin";
import { registerComponent, registerDockProvider } from "./registries";

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
    for (const [type, component] of Object.entries(plugin.components ?? {})) {
      registerComponent(type, component);
    }
    if (plugin.activate !== undefined || plugin.createDockProvider !== undefined) {
      const ctx = createCtx(plugin.id);
      if (plugin.createDockProvider !== undefined) {
        try {
          registerDockProvider(plugin.createDockProvider(ctx));
        } catch (error) {
          console.error(`plugin ${plugin.id} createDockProvider failed`, error);
        }
      }
      if (plugin.activate !== undefined) {
        Promise.resolve(plugin.activate(ctx)).catch((error: unknown) => {
          console.error(`plugin ${plugin.id} activate failed`, error);
        });
      }
    }
  }
}
