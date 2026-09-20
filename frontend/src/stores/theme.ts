/**
 * App theme system (v2, framework v0.80): a theme is four base colors
 * (canvas / surface / text / accent) plus optional advanced overrides.
 * Everything else — surface variants, muted text, borders, markdown and
 * syntax colors — derives from the base colors through the color-mix()
 * defaults declared on .app-shell in styles.css; scheme-dependent constants
 * (semantic hues, syntax palette, hover direction) gate on the data-theme
 * attribute, computed here from the canvas luminance. The store applies the
 * active theme as inline custom properties on .app-shell (base vars plus
 * overrides only) and persists themes to localStorage (viewer.themes.v2).
 * Built-in Light/Dark themes ship by default: editable and resettable, not
 * deletable; custom themes duplicate the active one and can be renamed and
 * deleted.
 *
 * Persistence (framework v0.81): theme DEFINITIONS live server-side in the
 * instance-store core plugin (plugin "shell", instance "themes", whole-state
 * {themes:[...]}), so custom themes follow the user across browsers and
 * machines; the `instance-store:shell:themes` mailbox propagates edits live
 * to other open browsers and every bus (re)connect triggers a full refresh
 * (server wins). The ACTIVE theme id is browser-local (localStorage
 * viewer.theme.active.v1), so each browser may default to a different theme.
 * localStorage viewer.themes.v2 remains as a boot cache (applied instantly
 * at startup, replaced by the server state on connect) and as the one-shot
 * seed source when the server has no record yet. Legacy keys
 * (viewer.themes.v1, viewer.markdownTheme.v1) are discarded on load.
 */
import { defineStore } from "pinia";

import { bus } from "../shell/bus";

/** Boot cache (browser-local copy of the server-held definitions). */
const CACHE_KEY = "viewer.themes.v2";
/** Browser-local active theme id (each browser may pick its own). */
const ACTIVE_KEY = "viewer.theme.active.v1";
const LEGACY_KEYS = ["viewer.themes.v1", "viewer.markdownTheme.v1"];
const SERVER_PLUGIN = "shell";
const SERVER_INSTANCE = "themes";
const MAILBOX = "instance-store:shell:themes";

export type ThemeScheme = "light" | "dark";

export interface BaseVars {
  canvas: string;
  surface: string;
  text: string;
  accent: string;
}

export type OverrideKind = "color" | "px" | "number";

export interface OverrideSpec {
  cssVar: string;
  kind: OverrideKind;
}

/** Advanced-override key -> the CSS custom property it drives. Keys not
 *  present in a theme's overrides map follow the styles.css derivation. */
export const OVERRIDE_VARS: Record<string, OverrideSpec> = {
  surfaceRaised: { cssVar: "--color-surface-raised", kind: "color" },
  surfaceMuted: { cssVar: "--color-surface-muted", kind: "color" },
  surfaceHover: { cssVar: "--color-surface-hover", kind: "color" },
  surfaceSelected: { cssVar: "--color-surface-selected", kind: "color" },
  titlebar: { cssVar: "--color-titlebar", kind: "color" },
  titlebarText: { cssVar: "--color-titlebar-text", kind: "color" },
  textMuted: { cssVar: "--color-text-muted", kind: "color" },
  textSubtle: { cssVar: "--color-text-subtle", kind: "color" },
  textInverse: { cssVar: "--color-text-inverse", kind: "color" },
  border: { cssVar: "--color-border", kind: "color" },
  borderStrong: { cssVar: "--color-border-strong", kind: "color" },
  accentHover: { cssVar: "--color-accent-hover", kind: "color" },
  accentSoft: { cssVar: "--color-accent-soft", kind: "color" },
  focus: { cssVar: "--color-focus", kind: "color" },
  success: { cssVar: "--color-success", kind: "color" },
  warning: { cssVar: "--color-warning", kind: "color" },
  danger: { cssVar: "--color-danger", kind: "color" },
  info: { cssVar: "--color-info", kind: "color" },
  overlay: { cssVar: "--color-overlay", kind: "color" },
  markdownBody: { cssVar: "--markdown-body-color", kind: "color" },
  markdownStrong: { cssVar: "--markdown-strong-color", kind: "color" },
  markdownLink: { cssVar: "--markdown-link-color", kind: "color" },
  markdownCodeColor: { cssVar: "--markdown-code-color", kind: "color" },
  markdownCodeBackground: { cssVar: "--markdown-code-background", kind: "color" },
  markdownBorder: { cssVar: "--markdown-border-color", kind: "color" },
  syntaxText: { cssVar: "--syntax-text", kind: "color" },
  syntaxBackground: { cssVar: "--syntax-background", kind: "color" },
  bodyFontSize: { cssVar: "--markdown-body-font-size", kind: "px" },
  bodyLineHeight: { cssVar: "--markdown-body-line-height", kind: "number" },
  codeFontSize: { cssVar: "--markdown-code-font-size", kind: "px" },
};

/** base var field -> CSS custom property it drives */
export const BASE_VAR_NAMES: Record<keyof BaseVars, string> = {
  canvas: "--color-canvas",
  surface: "--color-surface",
  text: "--color-text",
  accent: "--color-accent",
};

export interface ThemeDef {
  id: string;
  name: string;
  builtin: boolean;
  base: BaseVars;
  overrides: Record<string, string>;
}

const LIGHT_BASE: BaseVars = { canvas: "#ffffff", surface: "#ffffff", text: "#34383d", accent: "#58749a" };
const DARK_BASE: BaseVars = { canvas: "#111720", surface: "#111720", text: "#e6edf3", accent: "#58a6ff" };

function builtinThemes(): ThemeDef[] {
  return [
    { id: "light", name: "Light", builtin: true, base: { ...LIGHT_BASE }, overrides: {} },
    { id: "dark", name: "Dark", builtin: true, base: { ...DARK_BASE }, overrides: {} },
  ];
}

function builtinById(id: string): ThemeDef | undefined {
  return builtinThemes().find((t) => t.id === id);
}

/** Fill gaps / coerce a stored record into a valid ThemeDef. */
function normalizeTheme(raw: unknown, fallbackBase: BaseVars, builtin: boolean): ThemeDef {
  const record = (raw && typeof raw === "object" ? raw : {}) as Partial<ThemeDef>;
  const rawBase = (record.base && typeof record.base === "object" ? record.base : {}) as Partial<BaseVars>;
  const base = { ...fallbackBase };
  for (const field of Object.keys(BASE_VAR_NAMES) as Array<keyof BaseVars>) {
    const value = rawBase[field];
    if (typeof value === "string" && value !== "") base[field] = value;
  }
  const overrides: Record<string, string> = {};
  const rawOverrides = (record.overrides && typeof record.overrides === "object" ? record.overrides : {}) as Record<string, unknown>;
  for (const key of Object.keys(OVERRIDE_VARS)) {
    const value = rawOverrides[key];
    if (typeof value === "string" && value !== "") overrides[key] = value;
  }
  return {
    id: typeof record.id === "string" && record.id !== "" ? record.id : "custom",
    name: typeof record.name === "string" && record.name !== "" ? record.name : "自定义主题",
    builtin,
    base,
    overrides,
  };
}

/** Merge a stored/server theme list over the built-ins: built-ins always
 *  exist (edits persist, deletion is impossible); anything else is custom. */
function mergeThemes(stored: unknown[]): ThemeDef[] {
  const themes: ThemeDef[] = builtinThemes().map((def) => {
    const found = stored.find((t) => (t as Partial<ThemeDef>)?.id === def.id);
    return found ? { ...normalizeTheme(found, def.base, true), id: def.id } : def;
  });
  for (const t of stored) {
    const record = t as Partial<ThemeDef>;
    if (typeof record?.id !== "string" || record.id === "") continue;
    if (themes.some((x) => x.id === record.id)) continue;
    const theme = normalizeTheme(t, LIGHT_BASE, false);
    theme.id = record.id;
    themes.push(theme);
  }
  return themes;
}

/** Initial state: boot cache for definitions, browser-local key for active. */
function loadState(): { themes: ThemeDef[]; activeId: string } {
  for (const key of LEGACY_KEYS) localStorage.removeItem(key);
  let stored: unknown[] = [];
  let storedActive = "";
  try {
    const raw = localStorage.getItem(CACHE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as { themes?: unknown; activeId?: unknown };
      if (Array.isArray(parsed.themes)) stored = parsed.themes;
      // One-shot migration: v2 cache used to carry the active id.
      if (typeof parsed.activeId === "string") storedActive = parsed.activeId;
    }
  } catch {
    stored = [];
  }
  const activeRaw = localStorage.getItem(ACTIVE_KEY);
  if (activeRaw !== null && activeRaw !== "") storedActive = activeRaw;
  const themes = mergeThemes(stored);
  const activeId = themes.some((t) => t.id === storedActive) ? storedActive : "light";
  return { themes, activeId };
}

type ThemeStore = ReturnType<typeof useThemeStore>;

/** Fire-and-forget replication of the definitions to the server. */
function pushThemes(themes: ThemeDef[]): void {
  bus
    .request("instance:_:set", {
      plugin: SERVER_PLUGIN,
      instance: SERVER_INSTANCE,
      value: { themes },
    })
    .catch(() => undefined);
}

/** Server state replaces local definitions (server wins); the browser-local
 *  active id is kept unless it points at a now-deleted theme. */
function applyRemoteThemes(store: ThemeStore, remote: unknown[]): void {
  store.themes = mergeThemes(remote);
  if (!store.themes.some((t) => t.id === store.activeId)) {
    store.activeId = "light";
    localStorage.setItem(ACTIVE_KEY, store.activeId);
  }
  localStorage.setItem(CACHE_KEY, JSON.stringify({ themes: store.themes }));
  applyTheme(store.active);
}

/** Full refresh from the server; runs on init and on every (re)connect. */
async function refreshFromServer(store: ThemeStore): Promise<void> {
  try {
    const result = (await bus.request("instance:_:get", {
      plugin: SERVER_PLUGIN,
      instance: SERVER_INSTANCE,
    })) as { themes?: unknown } | null;
    if (result === null || typeof result !== "object" || Array.isArray(result)) {
      // No server record yet: the first browser with a boot cache seeds it;
      // browsers without a cache keep defaults until the record exists.
      if (localStorage.getItem(CACHE_KEY) !== null) pushThemes(store.themes);
      return;
    }
    applyRemoteThemes(store, Array.isArray(result.themes) ? result.themes : []);
  } catch {
    // Not connected yet (bootstrap order) or instance-store missing — the
    // next connect triggers another refresh.
  }
}

let syncStarted = false;

function targetElement(): HTMLElement | null {
  if (typeof document === "undefined") return null;
  return document.querySelector<HTMLElement>(".app-shell") ?? document.documentElement;
}

function hexToRgb(value: string): [number, number, number] | null {
  const match = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(value);
  if (!match) return null;
  return [parseInt(match[1], 16), parseInt(match[2], 16), parseInt(match[3], 16)];
}

/** Scheme follows the canvas: a dark canvas gets the dark-scheme constants
 *  (semantic hues, syntax palette, color-scheme) with no manual toggle. */
export function schemeOf(base: BaseVars): ThemeScheme {
  const rgb = hexToRgb(base.canvas);
  if (!rgb) return "light";
  const luminance = (0.2126 * rgb[0] + 0.7152 * rgb[1] + 0.0722 * rgb[2]) / 255;
  return luminance < 0.5 ? "dark" : "light";
}

function applyTheme(theme: ThemeDef): void {
  const element = targetElement();
  if (!element) return;
  const scheme = schemeOf(theme.base);
  element.setAttribute("data-theme", scheme);
  element.style.setProperty("color-scheme", scheme);
  for (const [field, cssVar] of Object.entries(BASE_VAR_NAMES) as Array<[keyof BaseVars, string]>) {
    element.style.setProperty(cssVar, theme.base[field]);
  }
  // Overrides: set the ones the theme defines, remove everything else so a
  // theme switch never leaves stale inline values behind.
  for (const [key, spec] of Object.entries(OVERRIDE_VARS)) {
    const value = theme.overrides[key];
    if (value === undefined) element.style.removeProperty(spec.cssVar);
    else element.style.setProperty(spec.cssVar, spec.kind === "px" ? `${value}px` : value);
  }
  // Bootstrap's primary color is an RGB triplet; derive it from the accent.
  const rgb = hexToRgb(theme.base.accent);
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
        builtin: false,
        base: { ...source.base },
        overrides: { ...source.overrides },
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
    setBase(id: string, field: keyof BaseVars, value: string): void {
      const theme = this.themes.find((t) => t.id === id);
      if (!theme || value.trim() === "") return;
      theme.base[field] = value;
      this.persist();
      if (id === this.activeId) applyTheme(theme);
    },
    /** Set (or clear, with undefined/"") an advanced override. */
    setOverride(id: string, key: string, value: string | undefined): void {
      const theme = this.themes.find((t) => t.id === id);
      if (!theme || !(key in OVERRIDE_VARS)) return;
      if (value === undefined || value.trim() === "") delete theme.overrides[key];
      else theme.overrides[key] = value;
      this.persist();
      if (id === this.activeId) applyTheme(theme);
    },
    /** Restore a built-in theme to its shipped name and base colors. */
    resetTheme(id: string): void {
      const index = this.themes.findIndex((t) => t.id === id);
      const def = builtinById(id);
      if (index < 0 || !def) return;
      this.themes[index] = def;
      this.persist();
      if (id === this.activeId) applyTheme(def);
    },
    /** Drop all advanced overrides of a theme (base colors are kept). */
    clearOverrides(id: string): void {
      const theme = this.themes.find((t) => t.id === id);
      if (!theme) return;
      theme.overrides = {};
      this.persist();
      if (id === this.activeId) applyTheme(theme);
    },
    /** Apply the active theme and start server sync; call once at startup. */
    init(): void {
      applyTheme(this.active);
      if (syncStarted) return;
      syncStarted = true;
      const store = this as ThemeStore;
      bus.subscribe(MAILBOX, (frame) => {
        const value = frame.value;
        if (value === null || typeof value !== "object" || Array.isArray(value)) return;
        const themes = (value as { themes?: unknown }).themes;
        if (Array.isArray(themes)) applyRemoteThemes(store, themes);
      });
      bus.onStateChange((connected) => {
        if (connected) void refreshFromServer(store);
      });
      void refreshFromServer(store);
    },
    /** Persist: active id stays browser-local; definitions go to the cache
     *  and replicate to the server (fire-and-forget, server reconciles). */
    persist(): void {
      localStorage.setItem(ACTIVE_KEY, this.activeId);
      localStorage.setItem(CACHE_KEY, JSON.stringify({ themes: this.themes }));
      pushThemes(this.themes);
    },
  },
});
