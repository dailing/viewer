import { defineStore } from "pinia";
import { getConfig, getTree, putConfig } from "../api/client";
import type { AppearanceConfig, CodexConfig, DirectoryListing, FileEntry, MarkdownConfig, MarkdownTheme, ViewerConfig, WorkspaceConfig } from "../types/files";

export const DEFAULT_MARKDOWN_THEME: MarkdownTheme = {
  name: "Default",
  body: { font_size: 15, color: "#172033", font_weight: null, line_height: 1.65 },
  h1: { font_size: 28, color: "#172033", font_weight: "700", line_height: 1.2 },
  h2: { font_size: 23, color: "#172033", font_weight: "700", line_height: 1.25 },
  h3: { font_size: 19, color: "#172033", font_weight: "700", line_height: 1.3 },
  h4: { font_size: 16, color: "#172033", font_weight: "700", line_height: 1.35 },
  paragraph: { font_size: 15, color: "#172033", font_weight: null, line_height: 1.65 },
  code: { font_size: 13, color: "#24292f", font_weight: null, line_height: null },
  code_background: "#f6f8fa",
  link_color: "#0969da",
  border_color: "#d0d7de",
  syntax: {
    background: "#f6f8fa",
    text: "#24292f",
    keyword: "#cf222e",
    string: "#0a3069",
    number: "#0550ae",
    title: "#8250df",
    comment: "#6e7781",
    meta: "#57606a",
  },
};

export const DEFAULT_APPEARANCE_CONFIG: AppearanceConfig = {
  navbar_size: 26,
};

export const DEFAULT_MARKDOWN_CONFIG: MarkdownConfig = {
  active_theme: DEFAULT_MARKDOWN_THEME.name,
  themes: [DEFAULT_MARKDOWN_THEME],
};

export const DEFAULT_CODEX_CONFIG: CodexConfig = {
  available_models: ["gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.5"],
  default_model: "gpt-5.5",
  proxy: "",
  muted_message_alpha: 0.56,
};

export const DEFAULT_WORKSPACE_CONFIG: WorkspaceConfig = {
  count: 5,
};

function cloneTheme(theme: MarkdownTheme): MarkdownTheme {
  return JSON.parse(JSON.stringify(theme)) as MarkdownTheme;
}

function normalizeAppearance(config?: Partial<AppearanceConfig>): AppearanceConfig {
  const size = Number(config?.navbar_size ?? DEFAULT_APPEARANCE_CONFIG.navbar_size);
  return { navbar_size: Math.min(56, Math.max(22, Number.isFinite(size) ? size : DEFAULT_APPEARANCE_CONFIG.navbar_size)) };
}

type MarkdownThemeInput = Partial<Omit<MarkdownTheme, "body" | "h1" | "h2" | "h3" | "h4" | "paragraph" | "code" | "syntax">> & {
  body?: Partial<MarkdownTheme["body"]>;
  h1?: Partial<MarkdownTheme["h1"]>;
  h2?: Partial<MarkdownTheme["h2"]>;
  h3?: Partial<MarkdownTheme["h3"]>;
  h4?: Partial<MarkdownTheme["h4"]>;
  paragraph?: Partial<MarkdownTheme["paragraph"]>;
  code?: Partial<MarkdownTheme["code"]>;
  syntax?: Partial<MarkdownTheme["syntax"]>;
};

function mergeTheme(theme?: MarkdownThemeInput): MarkdownTheme {
  const next = cloneTheme(DEFAULT_MARKDOWN_THEME);
  return {
    ...next,
    ...theme,
    body: { ...next.body, ...theme?.body },
    h1: { ...next.h1, ...theme?.h1 },
    h2: { ...next.h2, ...theme?.h2 },
    h3: { ...next.h3, ...theme?.h3 },
    h4: { ...next.h4, ...theme?.h4 },
    paragraph: { ...next.paragraph, ...theme?.paragraph },
    code: { ...next.code, ...theme?.code },
    syntax: { ...next.syntax, ...theme?.syntax },
  };
}

function normalizeMarkdown(config?: Partial<MarkdownConfig>): MarkdownConfig {
  const themes = config?.themes?.length ? config.themes.map((theme) => mergeTheme(theme as MarkdownThemeInput)) : [cloneTheme(DEFAULT_MARKDOWN_THEME)];
  const activeTheme = themes.some((theme) => theme.name === config?.active_theme) ? config?.active_theme : themes[0].name;
  return { active_theme: activeTheme ?? DEFAULT_MARKDOWN_THEME.name, themes };
}

function normalizeCodexConfig(config?: Partial<CodexConfig>): CodexConfig {
  const seen = new Set<string>();
  const available = (config?.available_models?.length ? config.available_models : DEFAULT_CODEX_CONFIG.available_models)
    .map((model) => model.trim())
    .filter((model) => {
      if (!model || seen.has(model)) return false;
      seen.add(model);
      return true;
    });
  const defaultModel = config?.default_model?.trim() || available[0] || DEFAULT_CODEX_CONFIG.default_model;
  return {
    available_models: available.includes(defaultModel) ? available : [defaultModel, ...available],
    default_model: defaultModel,
    proxy: config?.proxy?.trim() ?? DEFAULT_CODEX_CONFIG.proxy,
    muted_message_alpha: normalizeAlpha(config?.muted_message_alpha, DEFAULT_CODEX_CONFIG.muted_message_alpha),
  };
}

function normalizeAlpha(value: unknown, fallback: number): number {
  const alpha = Number(value ?? fallback);
  if (!Number.isFinite(alpha)) return fallback;
  return Math.min(1, Math.max(0.15, alpha));
}

function normalizeWorkspaceConfig(config?: Partial<WorkspaceConfig>): WorkspaceConfig {
  const count = Number(config?.count ?? DEFAULT_WORKSPACE_CONFIG.count);
  return { count: Math.min(20, Math.max(1, Math.round(Number.isFinite(count) ? count : DEFAULT_WORKSPACE_CONFIG.count))) };
}

export const useFilesStore = defineStore("files", {
  state: () => ({
    listings: {} as Record<string, DirectoryListing>,
    currentPath: "",
    expanded: new Set<string>(),
    pinned: [] as string[],
    visitTimes: {} as Record<string, number>,
    appearance: normalizeAppearance(),
    markdown: normalizeMarkdown(),
    codexConfig: normalizeCodexConfig(),
    workspaceConfig: normalizeWorkspaceConfig(),
    loading: false,
  }),
  getters: {
    rootEntries(state): FileEntry[] {
      return state.listings[""]?.entries ?? [];
    },
    currentEntries(state): FileEntry[] {
      const entries = [...(state.listings[state.currentPath]?.entries ?? [])];
      return entries.sort((left, right) => {
        const leftVisited = state.visitTimes[left.path] ?? 0;
        const rightVisited = state.visitTimes[right.path] ?? 0;
        if (leftVisited !== rightVisited) return rightVisited - leftVisited;
        if (left.is_dir !== right.is_dir) return left.is_dir ? -1 : 1;
        return left.name.localeCompare(right.name, undefined, { sensitivity: "base" });
      });
    },
    parentPath(state): string {
      if (!state.currentPath) return "";
      return state.currentPath.includes("/") ? state.currentPath.split("/").slice(0, -1).join("/") : "";
    },
    activeMarkdownTheme(state): MarkdownTheme {
      return state.markdown.themes.find((theme) => theme.name === state.markdown.active_theme) ?? state.markdown.themes[0] ?? DEFAULT_MARKDOWN_THEME;
    },
  },
  actions: {
    async loadConfig() {
      const config = await getConfig();
      this.appearance = normalizeAppearance(config.appearance);
      this.markdown = normalizeMarkdown(config.markdown);
      this.codexConfig = normalizeCodexConfig(config.codex);
      this.workspaceConfig = normalizeWorkspaceConfig(config.workspace);
    },
    async saveConfig() {
      const config: ViewerConfig = {
        appearance: normalizeAppearance(this.appearance),
        markdown: normalizeMarkdown(this.markdown),
        codex: normalizeCodexConfig(this.codexConfig),
        workspace: normalizeWorkspaceConfig(this.workspaceConfig),
      };
      const saved = await putConfig(config);
      this.appearance = normalizeAppearance(saved.appearance);
      this.markdown = normalizeMarkdown(saved.markdown);
      this.codexConfig = normalizeCodexConfig(saved.codex);
      this.workspaceConfig = normalizeWorkspaceConfig(saved.workspace);
    },
    async saveAppearance(appearance: AppearanceConfig) {
      this.appearance = normalizeAppearance(appearance);
      await this.saveConfig();
    },
    async saveMarkdown(markdown: MarkdownConfig) {
      this.markdown = normalizeMarkdown(markdown);
      await this.saveConfig();
    },
    async saveViewerConfig(appearance: AppearanceConfig, markdown: MarkdownConfig) {
      this.appearance = normalizeAppearance(appearance);
      this.markdown = normalizeMarkdown(markdown);
      await this.saveConfig();
    },
    async saveFullViewerConfig(appearance: AppearanceConfig, markdown: MarkdownConfig, codex: CodexConfig, workspace: WorkspaceConfig) {
      this.appearance = normalizeAppearance(appearance);
      this.markdown = normalizeMarkdown(markdown);
      this.codexConfig = normalizeCodexConfig(codex);
      this.workspaceConfig = normalizeWorkspaceConfig(workspace);
      await this.saveConfig();
    },
    async loadDirectory(path = "") {
      this.loading = true;
      try {
        this.listings[path] = await getTree(path);
      } finally {
        this.loading = false;
      }
    },
    async enterDirectory(path: string) {
      await this.loadDirectory(path);
      this.currentPath = path;
      this.visitTimes = { ...this.visitTimes, [path]: Date.now() / 1000 };
    },
    async recordVisit(path: string) {
      this.visitTimes = { ...this.visitTimes, [path]: Date.now() / 1000 };
    },
    async enterParentDirectory() {
      await this.enterDirectory(this.parentPath);
    },
    async toggleDirectory(path: string) {
      if (this.expanded.has(path)) {
        this.expanded.delete(path);
        return;
      }
      this.expanded.add(path);
      await this.loadDirectory(path);
    },
    async refreshAffected(path: string, isDir: boolean) {
      if (isDir && this.expanded.has(path)) await this.loadDirectory(path);
      const parent = path.includes("/") ? path.split("/").slice(0, -1).join("/") : "";
      if (this.listings[parent]) await this.loadDirectory(parent);
    },
    async togglePin(path: string) {
      if (this.pinned.includes(path)) {
        this.pinned = this.pinned.filter((item) => item !== path);
      } else {
        this.pinned = [path, ...this.pinned];
      }
    },
  },
});
