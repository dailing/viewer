/**
 * App theme system: named themes, each a complete set of the shell's
 * `--color-*` variables plus a light/dark base scheme. The active theme is
 * applied as inline custom properties on `.app-shell` (overriding the
 * styles.css defaults) and its scheme is mirrored to the `data-theme`
 * attribute, which drives the styles.css Markdown color fallback and
 * `color-scheme`. Themes are browser-local, persisted to localStorage
 * (viewer.themes.v1). Built-in Light/Dark themes ship by default: they can
 * be edited and reset, but not deleted; custom themes can be created
 * (duplicating the active theme), renamed, edited, and deleted.
 */
import { defineStore } from "pinia";

const STORAGE_KEY = "viewer.themes.v1";

export type ThemeScheme = "light" | "dark";

export interface ThemeVars {
  canvas: string;
  surface: string;
  surfaceRaised: string;
  surfaceMuted: string;
  surfaceHover: string;
  surfaceSelected: string;
  text: string;
  textMuted: string;
  textSubtle: string;
  textInverse: string;
  border: string;
  borderStrong: string;
  accent: string;
  accentHover: string;
  accentSoft: string;
  focus: string;
  success: string;
  warning: string;
  danger: string;
  info: string;
  overlay: string;
}

export interface ThemeDef {
  id: string;
  name: string;
  scheme: ThemeScheme;
  builtin: boolean;
  vars: ThemeVars;
}

/** theme var field -> CSS custom property it drives */
export const THEME_VAR_NAMES: Record<keyof ThemeVars, string> = {
  canvas: "--color-canvas",
  surface: "--color-surface",
  surfaceRaised: "--color-surface-raised",
  surfaceMuted: "--color-surface-muted",
  surfaceHover: "--color-surface-hover",
  surfaceSelected: "--color-surface-selected",
  text: "--color-text",
  textMuted: "--color-text-muted",
  textSubtle: "--color-text-subtle",
  textInverse: "--color-text-inverse",
  border: "--color-border",
  borderStrong: "--color-border-strong",
  accent: "--color-accent",
  accentHover: "--color-accent-hover",
  accentSoft: "--color-accent-soft",
  focus: "--color-focus",
  success: "--color-success",
  warning: "--color-warning",
  danger: "--color-danger",
  info: "--color-info",
  overlay: "--color-overlay",
};

const LIGHT_VARS: ThemeVars = {
  canvas: "#ffffff",
  surface: "#ffffff",
  surfaceRaised: "#f5f5f5",
  surfaceMuted: "#f5f5f5",
  surfaceHover: "#eeeeee",
  surfaceSelected: "#e9edf2",
  text: "#34383d",
  textMuted: "#747980",
  textSubtle: "#969ba1",
  textInverse: "#ffffff",
  border: "#e3e4e6",
  borderStrong: "#ced1d4",
  accent: "#58749a",
  accentHover: "#486487",
  accentSoft: "#edf0f4",
  focus: "#6b85a8",
  success: "#4f765f",
  warning: "#8a7047",
  danger: "#a05b57",
  info: "#557799",
  overlay: "rgb(15 23 42 / 0.38)",
};

const DARK_VARS: ThemeVars = {
  canvas: "#111720",
  surface: "#111720",
  surfaceRaised: "#171f2a",
  surfaceMuted: "#171f2a",
  surfaceHover: "#1d2937",
  surfaceSelected: "#102a43",
  text: "#e6edf3",
  textMuted: "#9aa7b6",
  textSubtle: "#748094",
  textInverse: "#07111f",
  border: "#303a47",
  borderStrong: "#4b596a",
  accent: "#58a6ff",
  accentHover: "#79c0ff",
  accentSoft: "#102a43",
  focus: "#58a6ff",
  success: "#56d364",
  warning: "#e3b341",
  danger: "#ff7b72",
  info: "#79c0ff",
  overlay: "rgb(0 0 0 / 0.58)",
};

function builtinThemes(): ThemeDef[] {
  return [
    { id: "light", name: "Light", scheme: "light", builtin: true, vars: { ...LIGHT_VARS } },
    { id: "dark", name: "Dark", scheme: "dark", builtin: true, vars: { ...DARK_VARS } },
  ];
}

function builtinById(id: string): ThemeDef | undefined {
  return builtinThemes().find((t) => t.id === id);
}

function baseForScheme(scheme: ThemeScheme): ThemeDef {
  return scheme === "dark" ? builtinThemes()[1] : builtinThemes()[0];
}

/** Fill gaps / coerce a stored record into a valid ThemeDef. */
function normalizeTheme(raw: unknown, base: ThemeDef): ThemeDef {
  const record = (raw && typeof raw === "object" ? raw : {}) as Partial<ThemeDef>;
  const scheme: ThemeScheme = record.scheme === "dark" ? "dark" : record.scheme === "light" ? "light" : base.scheme;
  const rawVars = (record.vars && typeof record.vars === "object" ? record.vars : {}) as Partial<ThemeVars>;
  const vars = { ...base.vars };
  for (const field of Object.keys(THEME_VAR_NAMES) as Array<keyof ThemeVars>) {
    const value = rawVars[field];
    if (typeof value === "string" && value !== "") vars[field] = value;
  }
  return {
    id: typeof record.id === "string" && record.id !== "" ? record.id : base.id,
    name: typeof record.name === "string" && record.name !== "" ? record.name : base.name,
    scheme,
    builtin: base.builtin,
    vars,
  };
}

function loadState(): { themes: ThemeDef[]; activeId: string } {
  const defaults = builtinThemes();
  let stored: unknown[] = [];
  let storedActive = "";
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as { themes?: unknown; activeId?: unknown };
      if (Array.isArray(parsed.themes)) stored = parsed.themes;
      if (typeof parsed.activeId === "string") storedActive = parsed.activeId;
    }
  } catch {
    stored = [];
    storedActive = "";
  }
  // Built-ins always exist (edits persist, deletion is impossible); anything
  // else in storage is a custom theme.
  const themes: ThemeDef[] = defaults.map((def) => {
    const found = stored.find((t) => (t as Partial<ThemeDef>)?.id === def.id);
    return found ? normalizeTheme(found, def) : def;
  });
  for (const t of stored) {
    const record = t as Partial<ThemeDef>;
    if (typeof record?.id !== "string" || record.id === "") continue;
    if (themes.some((x) => x.id === record.id)) continue;
    const scheme: ThemeScheme = record.scheme === "dark" ? "dark" : "light";
    themes.push(normalizeTheme(t, { ...baseForScheme(scheme), id: record.id, builtin: false }));
  }
  const activeId = themes.some((t) => t.id === storedActive) ? storedActive : "light";
  return { themes, activeId };
}

function targetElement(): HTMLElement | null {
  if (typeof document === "undefined") return null;
  return document.querySelector<HTMLElement>(".app-shell") ?? document.documentElement;
}

function hexToRgb(value: string): [number, number, number] | null {
  const match = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(value);
  if (!match) return null;
  return [parseInt(match[1], 16), parseInt(match[2], 16), parseInt(match[3], 16)];
}

function applyTheme(theme: ThemeDef): void {
  const element = targetElement();
  if (!element) return;
  element.setAttribute("data-theme", theme.scheme);
  element.style.setProperty("color-scheme", theme.scheme);
  for (const [field, cssVar] of Object.entries(THEME_VAR_NAMES) as Array<[keyof ThemeVars, string]>) {
    element.style.setProperty(cssVar, theme.vars[field]);
  }
  // Bootstrap's primary color is an RGB triplet; derive it from the accent.
  const rgb = hexToRgb(theme.vars.accent);
  if (rgb) element.style.setProperty("--bs-primary-rgb", `${rgb[0]}, ${rgb[1]}, ${rgb[2]}`);
}

export const useThemeStore = defineStore("theme", {
  state: () => loadState(),
  getters: {
    active(state): ThemeDef {
      return state.themes.find((t) => t.id === state.activeId) ?? state.themes[0];
    },
  },
  actions: {
    setActive(id: string): void {
      if (!this.themes.some((t) => t.id === id)) return;
      this.activeId = id;
      this.persist();
      applyTheme(this.active);
    },
    /** Duplicate `fromId` (default: active theme) under a new id; activates it. */
    createTheme(name: string, fromId?: string): string {
      const source = this.themes.find((t) => t.id === fromId) ?? this.active;
      const id = `custom-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`;
      this.themes.push({
        id,
        name: name.trim() || "自定义主题",
        scheme: source.scheme,
        builtin: false,
        vars: { ...source.vars },
      });
      this.setActive(id);
      return id;
    },
    deleteTheme(id: string): void {
      const theme = this.themes.find((t) => t.id === id);
      if (!theme || theme.builtin) return;
      this.themes = this.themes.filter((t) => t.id !== id);
      if (this.activeId === id) {
        this.setActive("light");
        return;
      }
      this.persist();
    },
    renameTheme(id: string, name: string): void {
      const theme = this.themes.find((t) => t.id === id);
      const trimmed = name.trim();
      if (!theme || trimmed === "") return;
      theme.name = trimmed;
      this.persist();
    },
    setScheme(id: string, scheme: ThemeScheme): void {
      const theme = this.themes.find((t) => t.id === id);
      if (!theme) return;
      theme.scheme = scheme;
      this.persist();
      if (id === this.activeId) applyTheme(theme);
    },
    setVar(id: string, field: keyof ThemeVars, value: string): void {
      const theme = this.themes.find((t) => t.id === id);
      if (!theme || value.trim() === "") return;
      theme.vars[field] = value;
      this.persist();
      if (id === this.activeId) applyTheme(theme);
    },
    /** Restore a built-in theme to its shipped name/scheme/colors. */
    resetTheme(id: string): void {
      const index = this.themes.findIndex((t) => t.id === id);
      const def = builtinById(id);
      if (index < 0 || !def) return;
      this.themes[index] = def;
      this.persist();
      if (id === this.activeId) applyTheme(def);
    },
    /** Apply the persisted active theme; call once at app startup. */
    init(): void {
      applyTheme(this.active);
    },
    persist(): void {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ activeId: this.activeId, themes: this.themes }));
    },
  },
});
