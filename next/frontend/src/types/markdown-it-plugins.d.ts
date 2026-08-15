// Ambient declarations for markdown-it plugins without published type packages.
// (anchor/attrs ship their own types; markdown-it/footnote use @types/*.)
// NOTE: no top-level import/export here — this file must stay a global script
// so the `declare module` blocks are ambient declarations, not augmentations.

declare module "markdown-it-deflist" {
  const plugin: import("markdown-it").PluginSimple;
  export default plugin;
}
declare module "markdown-it-mark" {
  const plugin: import("markdown-it").PluginSimple;
  export default plugin;
}
declare module "markdown-it-sub" {
  const plugin: import("markdown-it").PluginSimple;
  export default plugin;
}
declare module "markdown-it-sup" {
  const plugin: import("markdown-it").PluginSimple;
  export default plugin;
}
declare module "markdown-it-task-lists" {
  const plugin: import("markdown-it").PluginWithOptions<{ enabled?: boolean }>;
  export default plugin;
}
declare module "markdown-it-texmath" {
  const plugin: import("markdown-it").PluginWithOptions<{
    engine?: unknown;
    delimiters?: string | string[];
    katexOptions?: Record<string, unknown>;
  }>;
  export default plugin;
}
