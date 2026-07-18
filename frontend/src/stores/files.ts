import { defineStore } from "pinia";
import { deleteFile, getConfig, getTree, putConfig, uploadFile } from "../api/client";
import type { AppearanceConfig, CodexConfig, DirectoryListing, FileEntry, MarkdownConfig, MarkdownTheme, SuperWorkspaceConfig, SuperWorkspaceDispatchProfile, ViewerConfig, VoiceConfig } from "../types/files";
import { parentPath as resolveParentPath } from "../utils/paths";
import { storageKey } from "../utils/storage";

const PINNED_FILES_KEY = "viewer.pinnedFiles.v1";

export const DEFAULT_MARKDOWN_THEME: MarkdownTheme = {
  name: "Default",
  body: { font_size: 15, color: "#404449", font_weight: null, line_height: 1.65 },
  h1: { font_size: 28, color: "#30343a", font_weight: "700", line_height: 1.2 },
  h2: { font_size: 23, color: "#30343a", font_weight: "700", line_height: 1.25 },
  h3: { font_size: 19, color: "#34383d", font_weight: "700", line_height: 1.3 },
  h4: { font_size: 16, color: "#34383d", font_weight: "700", line_height: 1.35 },
  paragraph: { font_size: 15, color: "#404449", font_weight: null, line_height: 1.65 },
  code: { font_size: 13, color: "#4a4e53", font_weight: null, line_height: null },
  code_background: "#f5f5f5",
  link_color: "#58749a",
  border_color: "#e3e4e6",
  syntax: {
    background: "#f5f5f5",
    text: "#4a4e53",
    keyword: "#8f5f63",
    string: "#55706b",
    number: "#627796",
    title: "#766b8c",
    comment: "#8a8e93",
    meta: "#72777c",
  },
};

export const DARK_MARKDOWN_THEME: MarkdownTheme = {
  name: "Dark",
  body: { font_size: 15, color: "#d8dee9", font_weight: null, line_height: 1.65 },
  h1: { font_size: 28, color: "#f8fafc", font_weight: "700", line_height: 1.2 },
  h2: { font_size: 23, color: "#f1f5f9", font_weight: "700", line_height: 1.25 },
  h3: { font_size: 19, color: "#e5edf7", font_weight: "700", line_height: 1.3 },
  h4: { font_size: 16, color: "#dbe7f3", font_weight: "700", line_height: 1.35 },
  paragraph: { font_size: 15, color: "#d8dee9", font_weight: null, line_height: 1.65 },
  code: { font_size: 13, color: "#e6edf3", font_weight: null, line_height: null },
  code_background: "#161b22",
  link_color: "#79c0ff",
  border_color: "#30363d",
  syntax: {
    background: "#0d1117",
    text: "#e6edf3",
    keyword: "#ff7b72",
    string: "#a5d6ff",
    number: "#79c0ff",
    title: "#d2a8ff",
    comment: "#8b949e",
    meta: "#ffa657",
  },
};

const BUILTIN_MARKDOWN_THEMES = [DEFAULT_MARKDOWN_THEME, DARK_MARKDOWN_THEME];

export const DEFAULT_APPEARANCE_CONFIG: AppearanceConfig = {
  color_theme: "system",
  density: "compact",
};

export const DEFAULT_MARKDOWN_CONFIG: MarkdownConfig = {
  active_theme: DEFAULT_MARKDOWN_THEME.name,
  follow_app_theme: true,
  themes: BUILTIN_MARKDOWN_THEMES,
};

export const DEFAULT_CODEX_CONFIG: CodexConfig = {
  available_models: ["gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.3-codex", "gpt-5.3-codex-spark"],
  default_model: "gpt-5.5",
  proxy: "",
  muted_message_alpha: 0.56,
};

export const DEFAULT_VOICE_CONFIG: VoiceConfig = {
  enabled: true,
  language_model_refine: true,
  available_models: ["large-v3-turbo", "small", "medium", "base", "tiny"],
  model: "large-v3-turbo",
  available_languages: ["auto", "en", "zh", "ja", "ko", "fr", "de", "es"],
  language: "auto",
  translation_enabled: false,
  available_target_languages: ["en", "zh", "ja", "ko", "fr", "de", "es"],
  target_language: "en",
};

export const DEFAULT_DISPATCH_PROFILES: SuperWorkspaceDispatchProfile[] = [
  {
    id: "local-vllm",
    name: "Local vLLM",
    api_url: "http://127.0.0.1:8010/v1/chat/completions",
    model: "qwen3-14b",
    api_key: "",
  },
  {
    id: "deepseek",
    name: "DeepSeek",
    api_url: "https://api.deepseek.com/v1/chat/completions",
    model: "deepseek-v4-flash",
    api_key: "",
  },
];

export const DEFAULT_DISPATCH_PROMPT_TEMPLATE = `You route one user message to persistent agent roles.

Default to exactly one role.
Choose multiple roles only when the user explicitly asks for multiple independent tasks, or when no single role can reasonably complete the request.

Use recent visible chat history only to understand context, not as a separate task.
Return only JSON:
{"role_ids":["role-id"],"rationale":"short reason"}

Available roles:
{{roles_json}}

Recent visible chat history:
{{history}}

Current message:
{{message}}`;

export const DEFAULT_SUPER_WORKSPACE_CONFIG: SuperWorkspaceConfig = {
  hindsight_retain_enabled: true,
  hindsight_api_url: "",
  hindsight_bank_prefix: "super-workspace",
  chat_history_bootstrap_enabled: true,
  chat_history_bootstrap_tokens: 5000,
  active_dispatch_profile_id: "local-vllm",
  dispatch_history_word_budget: 2048,
  dispatch_prompt_template: DEFAULT_DISPATCH_PROMPT_TEMPLATE,
  dispatch_profiles: DEFAULT_DISPATCH_PROFILES,
};

function cloneTheme(theme: MarkdownTheme): MarkdownTheme {
  return JSON.parse(JSON.stringify(theme)) as MarkdownTheme;
}

function normalizeAppearance(config?: Partial<AppearanceConfig>): AppearanceConfig {
  const colorTheme = config?.color_theme;
  const density = config?.density;
  return {
    color_theme: colorTheme === "light" || colorTheme === "dark" ? colorTheme : "system",
    density: density === "comfortable" ? "comfortable" : "compact",
  };
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
  for (const builtin of BUILTIN_MARKDOWN_THEMES) {
    if (!themes.some((theme) => theme.name === builtin.name)) themes.push(cloneTheme(builtin));
  }
  const activeTheme = themes.some((theme) => theme.name === config?.active_theme) ? config?.active_theme : themes[0].name;
  return {
    active_theme: activeTheme ?? DEFAULT_MARKDOWN_THEME.name,
    follow_app_theme: config?.follow_app_theme ?? true,
    themes,
  };
}

function normalizeCodexConfig(config?: Partial<CodexConfig>): CodexConfig {
  const seen = new Set<string>();
  const sourceModels = [...(config?.available_models?.length ? config.available_models : []), ...DEFAULT_CODEX_CONFIG.available_models];
  const available = sourceModels
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

function uniqueCleanList(items: string[] | undefined, fallback: string[], options?: { lowercase?: boolean }) {
  const seen = new Set<string>();
  const source = items?.length ? items : fallback;
  return source
    .map((item) => (options?.lowercase ? item.trim().toLowerCase() : item.trim()))
    .filter((item) => {
      if (!item || seen.has(item)) return false;
      seen.add(item);
      return true;
    });
}

function normalizeVoiceConfig(config?: Partial<VoiceConfig>): VoiceConfig {
  const models = uniqueCleanList(config?.available_models, DEFAULT_VOICE_CONFIG.available_models);
  const languages = uniqueCleanList(config?.available_languages, DEFAULT_VOICE_CONFIG.available_languages, { lowercase: true });
  const targetLanguages = uniqueCleanList(config?.available_target_languages, DEFAULT_VOICE_CONFIG.available_target_languages, { lowercase: true }).filter(
    (item) => item !== "auto",
  );
  const model = (config?.model?.trim() || models[0] || DEFAULT_VOICE_CONFIG.model);
  const language = config?.language?.trim().toLowerCase() || DEFAULT_VOICE_CONFIG.language;
  const targetLanguage = config?.target_language?.trim().toLowerCase() || DEFAULT_VOICE_CONFIG.target_language;
  return {
    enabled: config?.enabled ?? DEFAULT_VOICE_CONFIG.enabled,
    language_model_refine: config?.language_model_refine ?? DEFAULT_VOICE_CONFIG.language_model_refine,
    available_models: models.includes(model) ? models : [model, ...models],
    model,
    available_languages: languages.includes(language) ? languages : [language, ...languages],
    language,
    translation_enabled: config?.translation_enabled ?? DEFAULT_VOICE_CONFIG.translation_enabled,
    available_target_languages: targetLanguages.includes(targetLanguage) ? targetLanguages : [targetLanguage, ...targetLanguages],
    target_language: targetLanguage,
  };
}

function normalizeAlpha(value: unknown, fallback: number): number {
  const alpha = Number(value ?? fallback);
  if (!Number.isFinite(alpha)) return fallback;
  return Math.min(1, Math.max(0.15, alpha));
}

function normalizeSuperWorkspaceConfig(config?: Partial<SuperWorkspaceConfig>): SuperWorkspaceConfig {
  const tokens = Number(config?.chat_history_bootstrap_tokens ?? DEFAULT_SUPER_WORKSPACE_CONFIG.chat_history_bootstrap_tokens);
  const dispatchBudget = Number(config?.dispatch_history_word_budget ?? DEFAULT_SUPER_WORKSPACE_CONFIG.dispatch_history_word_budget);
  const profiles = normalizeDispatchProfiles(config?.dispatch_profiles);
  const activeProfileId = profiles.some((profile) => profile.id === config?.active_dispatch_profile_id)
    ? String(config?.active_dispatch_profile_id)
    : profiles[0]?.id ?? DEFAULT_SUPER_WORKSPACE_CONFIG.active_dispatch_profile_id;
  return {
    hindsight_retain_enabled: config?.hindsight_retain_enabled ?? DEFAULT_SUPER_WORKSPACE_CONFIG.hindsight_retain_enabled,
    hindsight_api_url: config?.hindsight_api_url?.trim() ?? DEFAULT_SUPER_WORKSPACE_CONFIG.hindsight_api_url,
    hindsight_bank_prefix: config?.hindsight_bank_prefix?.trim() || DEFAULT_SUPER_WORKSPACE_CONFIG.hindsight_bank_prefix,
    chat_history_bootstrap_enabled: config?.chat_history_bootstrap_enabled ?? DEFAULT_SUPER_WORKSPACE_CONFIG.chat_history_bootstrap_enabled,
    chat_history_bootstrap_tokens: Math.min(50000, Math.max(0, Number.isFinite(tokens) ? Math.floor(tokens) : DEFAULT_SUPER_WORKSPACE_CONFIG.chat_history_bootstrap_tokens)),
    active_dispatch_profile_id: activeProfileId,
    dispatch_history_word_budget: Math.min(50000, Math.max(0, Number.isFinite(dispatchBudget) ? Math.floor(dispatchBudget) : DEFAULT_SUPER_WORKSPACE_CONFIG.dispatch_history_word_budget)),
    dispatch_prompt_template: config?.dispatch_prompt_template?.trim() || DEFAULT_SUPER_WORKSPACE_CONFIG.dispatch_prompt_template,
    dispatch_profiles: profiles,
  };
}

function normalizeDispatchProfiles(profiles?: Partial<SuperWorkspaceDispatchProfile>[]): SuperWorkspaceDispatchProfile[] {
  const seen = new Set<string>();
  const source = profiles?.length ? profiles : DEFAULT_DISPATCH_PROFILES;
  const normalized = source
    .map((profile, index) => {
      const id = (profile.id?.trim() || profile.name?.trim().toLowerCase().replace(/[^a-z0-9_-]+/g, "-") || `dispatch-${index + 1}`).replace(/^-+|-+$/g, "");
      return {
        id: id || `dispatch-${index + 1}`,
        name: profile.name?.trim() || id || `Dispatch ${index + 1}`,
        api_url: profile.api_url?.trim() || DEFAULT_DISPATCH_PROFILES[0].api_url,
        model: profile.model?.trim() || "",
        api_key: profile.api_key?.trim() || "",
      };
    })
    .filter((profile) => {
      if (!profile.id || seen.has(profile.id)) return false;
      seen.add(profile.id);
      return true;
    });
  for (const builtin of DEFAULT_DISPATCH_PROFILES) {
    if (!normalized.some((profile) => profile.id === builtin.id)) normalized.push({ ...builtin });
  }
  return normalized;
}

function readPinnedFiles(): string[] {
  try {
    const raw = localStorage.getItem(storageKey(PINNED_FILES_KEY));
    const parsed = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) return [];
    const seen = new Set<string>();
    return parsed
      .filter((item): item is string => typeof item === "string" && item.trim().length > 0)
      .filter((item) => {
        if (seen.has(item)) return false;
        seen.add(item);
        return true;
      });
  } catch {
    return [];
  }
}

function writePinnedFiles(paths: string[]) {
  localStorage.setItem(storageKey(PINNED_FILES_KEY), JSON.stringify(paths));
}

export const useFilesStore = defineStore("files", {
  state: () => ({
    listings: {} as Record<string, DirectoryListing>,
    currentPath: "",
    expanded: new Set<string>(),
    pinned: readPinnedFiles(),
    visitTimes: {} as Record<string, number>,
    visitStack: [] as string[],
    appearance: normalizeAppearance(),
    markdown: normalizeMarkdown(),
    codexConfig: normalizeCodexConfig(),
    voiceConfig: normalizeVoiceConfig(),
    superWorkspaceConfig: normalizeSuperWorkspaceConfig(),
    loading: false,
  }),
  getters: {
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
      return resolveParentPath(state.currentPath);
    },
  },
  actions: {
    async loadConfig() {
      const config = await getConfig();
      this.appearance = normalizeAppearance(config.appearance);
      this.markdown = normalizeMarkdown(config.markdown);
      this.codexConfig = normalizeCodexConfig(config.codex);
      this.voiceConfig = normalizeVoiceConfig(config.voice);
      this.superWorkspaceConfig = normalizeSuperWorkspaceConfig(config.super_workspace);
    },
    async saveConfig() {
      const config: ViewerConfig = {
        appearance: normalizeAppearance(this.appearance),
        markdown: normalizeMarkdown(this.markdown),
        codex: normalizeCodexConfig(this.codexConfig),
        voice: normalizeVoiceConfig(this.voiceConfig),
        super_workspace: normalizeSuperWorkspaceConfig(this.superWorkspaceConfig),
      };
      const saved = await putConfig(config);
      this.appearance = normalizeAppearance(saved.appearance);
      this.markdown = normalizeMarkdown(saved.markdown);
      this.codexConfig = normalizeCodexConfig(saved.codex);
      this.voiceConfig = normalizeVoiceConfig(saved.voice);
      this.superWorkspaceConfig = normalizeSuperWorkspaceConfig(saved.super_workspace);
    },
    async saveFullViewerConfig(appearance: AppearanceConfig, markdown: MarkdownConfig, codex: CodexConfig, voice: VoiceConfig, superWorkspace: SuperWorkspaceConfig) {
      this.appearance = normalizeAppearance(appearance);
      this.markdown = normalizeMarkdown(markdown);
      this.codexConfig = normalizeCodexConfig(codex);
      this.voiceConfig = normalizeVoiceConfig(voice);
      this.superWorkspaceConfig = normalizeSuperWorkspaceConfig(superWorkspace);
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
      if (this.currentPath && this.currentPath !== path) {
        this.visitStack.push(this.currentPath);
      }
      await this.loadDirectory(path);
      this.currentPath = path;
      this.visitTimes = { ...this.visitTimes, [path]: Date.now() / 1000 };
    },
    async recordVisit(path: string) {
      this.visitTimes = { ...this.visitTimes, [path]: Date.now() / 1000 };
    },
    async enterParentDirectory() {
      // Pop the visit stack; every directory entry pushed one, so every
      // ".." click pops one. This naturally handles symlink targets and
      // normal subdirectories with the same code path.
      if (this.visitStack.length > 0) {
        const previous = this.visitStack.pop()!;
        await this.enterDirectory(previous);
        return;
      }
      // Stack empty: nowhere to go back to.
    },
    async refreshAffected(path: string, isDir: boolean) {
      if (isDir && this.expanded.has(path)) await this.loadDirectory(path);
      const parent = resolveParentPath(path);
      if (this.listings[parent]) await this.loadDirectory(parent);
    },
    async uploadToCurrent(files: File[]) {
      for (const file of files) {
        await uploadFile(this.currentPath, file);
      }
      await this.loadDirectory(this.currentPath);
    },
    async deletePath(path: string) {
      await deleteFile(path);
      this.pinned = this.pinned.filter((item) => item !== path);
      writePinnedFiles(this.pinned);
      const parent = resolveParentPath(path);
      await this.loadDirectory(parent);
      if (this.currentPath !== parent && this.listings[this.currentPath]) await this.loadDirectory(this.currentPath);
    },
    async togglePin(path: string) {
      if (this.pinned.includes(path)) {
        this.pinned = this.pinned.filter((item) => item !== path);
      } else {
        this.pinned = [path, ...this.pinned];
      }
      writePinnedFiles(this.pinned);
    },
  },
});
