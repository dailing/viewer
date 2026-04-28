import { defineStore } from "pinia";
import { getConfig, getTree, putConfig } from "../api/client";
import type { AppearanceConfig, DirectoryListing, FileEntry, MarkdownConfig, MarkdownTheme, ViewerConfig } from "../types/files";

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

export const useFilesStore = defineStore("files", {
  state: () => ({
    listings: {} as Record<string, DirectoryListing>,
    currentPath: "",
    expanded: new Set<string>(),
    pinned: [] as string[],
    appearance: normalizeAppearance(),
    markdown: normalizeMarkdown(),
    loading: false,
  }),
  getters: {
    rootEntries(state): FileEntry[] {
      return state.listings[""]?.entries ?? [];
    },
    currentEntries(state): FileEntry[] {
      return state.listings[state.currentPath]?.entries ?? [];
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
      this.pinned = config.pinned;
      this.currentPath = config.current_path;
      this.appearance = normalizeAppearance(config.appearance);
      this.markdown = normalizeMarkdown(config.markdown);
    },
    async saveConfig() {
      const config: ViewerConfig = {
        pinned: this.pinned,
        current_path: this.currentPath,
        appearance: normalizeAppearance(this.appearance),
        markdown: normalizeMarkdown(this.markdown),
      };
      const saved = await putConfig(config);
      this.pinned = saved.pinned;
      this.currentPath = saved.current_path;
      this.appearance = normalizeAppearance(saved.appearance);
      this.markdown = normalizeMarkdown(saved.markdown);
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
      await this.saveConfig();
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
      await this.saveConfig();
    },
  },
});
