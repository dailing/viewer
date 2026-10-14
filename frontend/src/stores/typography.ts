/**
 * Browser-local typography (framework v0.88): two base font sizes — UI
 * (--font-size-ui, chrome and controls) and content
 * (--markdown-body-font-size, chat message / markdown document body) —
 * applied as inline custom properties on .app-shell. Every other size
 * derives from the bases through exact calc() coefficients declared on
 * .app-shell in styles.css (and component styles), and each derived size
 * can be pinned to an explicit px via the 细节定制 settings (details map),
 * winning over the coefficient default.
 *
 * Unlike theme DEFINITIONS (server-synced, cross-browser), font size is a
 * per-browser preference — screens and reading distances differ per
 * machine — so the whole record lives in localStorage
 * (viewer.typography.v1) and nothing replicates.
 *
 * Defaults reproduce the pre-v0.88 fixed sizes exactly (12/15 bases; exact
 * fractions such as 11/12 and 28/15 as coefficients), so an upgraded
 * browser renders identically until the user changes a value.
 */
import { defineStore } from "pinia";

const STORAGE_KEY = "viewer.typography.v1";

export const DEFAULT_UI_SIZE = 12;
export const DEFAULT_CONTENT_SIZE = 15;

export const UI_SIZE_RANGE = { min: 9, max: 24 } as const;
export const CONTENT_SIZE_RANGE = { min: 10, max: 32 } as const;
export const DETAIL_SIZE_RANGE = { min: 7, max: 48 } as const;

export type TypoBase = "ui" | "content";

export interface TypoDetailSpec {
  cssVar: string;
  label: string;
  base: TypoBase;
  /** Coefficient off the base when the detail is not pinned — mirrors the
   *  calc() defaults in styles.css (kept in sync deliberately, like the
   *  theme NUMBER_SEEDS). */
  ratio: number;
}

/** 细节定制 key -> the derived CSS custom property it pins. */
export const TYPO_DETAILS: Record<string, TypoDetailSpec> = {
  uiSmall: { cssVar: "--font-size-ui-small", label: "界面小字", base: "ui", ratio: 11 / 12 },
  h1: { cssVar: "--markdown-h1-font-size", label: "标题 H1", base: "content", ratio: 28 / 15 },
  h2: { cssVar: "--markdown-h2-font-size", label: "标题 H2", base: "content", ratio: 23 / 15 },
  h3: { cssVar: "--markdown-h3-font-size", label: "标题 H3", base: "content", ratio: 19 / 15 },
  h4: { cssVar: "--markdown-h4-font-size", label: "标题 H4", base: "content", ratio: 16 / 15 },
  code: { cssVar: "--markdown-code-font-size", label: "代码字号", base: "content", ratio: 13 / 15 },
};

interface TypographyState {
  /** null = default (12 / 15). */
  ui: number | null;
  content: number | null;
  /** Pinned derived sizes (px); absent keys follow the coefficient. */
  details: Record<string, number>;
}

function clamp(value: number, range: { min: number; max: number }): number {
  return Math.min(range.max, Math.max(range.min, value));
}

function coerceSize(raw: unknown, range: { min: number; max: number }): number | null {
  const value = typeof raw === "string" ? Number(raw) : raw;
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) return null;
  return clamp(Math.round(value * 10) / 10, range);
}

function loadState(): TypographyState {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw !== null) {
      const parsed = JSON.parse(raw) as Partial<TypographyState>;
      const details: Record<string, number> = {};
      const rawDetails = (parsed.details && typeof parsed.details === "object" ? parsed.details : {}) as Record<string, unknown>;
      for (const key of Object.keys(TYPO_DETAILS)) {
        const value = coerceSize(rawDetails[key], DETAIL_SIZE_RANGE);
        if (value !== null) details[key] = value;
      }
      return {
        ui: coerceSize(parsed.ui, UI_SIZE_RANGE),
        content: coerceSize(parsed.content, CONTENT_SIZE_RANGE),
        details,
      };
    }
  } catch {
    // Corrupt entry — fall through to defaults.
  }
  return { ui: null, content: null, details: {} };
}

function targetElement(): HTMLElement | null {
  if (typeof document === "undefined") return null;
  return document.querySelector<HTMLElement>(".app-shell") ?? document.documentElement;
}

/** Round to one decimal for display / pinning seeds. */
function round1(value: number): number {
  return Math.round(value * 10) / 10;
}

export const useTypographyStore = defineStore("typography", {
  state: (): TypographyState => loadState(),
  getters: {
    uiSize(state): number {
      return state.ui ?? DEFAULT_UI_SIZE;
    },
    contentSize(state): number {
      return state.content ?? DEFAULT_CONTENT_SIZE;
    },
    /** Effective px for a derived size: pinned value, else base × ratio. */
    detailSize(state): (key: string) => number {
      return (key: string) => {
        const spec = TYPO_DETAILS[key];
        const pinned = state.details[key];
        if (spec === undefined) return pinned ?? 0;
        if (pinned !== undefined) return pinned;
        const base = spec.base === "ui" ? (state.ui ?? DEFAULT_UI_SIZE) : (state.content ?? DEFAULT_CONTENT_SIZE);
        return round1(base * spec.ratio);
      };
    },
    /** xterm font size: tracks the content base (13px at the default 15). */
    terminalSize(state): number {
      const base = state.content ?? DEFAULT_CONTENT_SIZE;
      return Math.max(6, Math.round((base * 13) / 15));
    },
    detailCount(state): number {
      return Object.keys(state.details).length;
    },
    customized(state): boolean {
      return state.ui !== null || state.content !== null || Object.keys(state.details).length > 0;
    },
  },
  actions: {
    setUi(value: number | null): void {
      this.ui = coerceSize(value, UI_SIZE_RANGE);
      this.commit();
    },
    setContent(value: number | null): void {
      this.content = coerceSize(value, CONTENT_SIZE_RANGE);
      this.commit();
    },
    /** Pin a derived size to an explicit px; null restores the coefficient. */
    setDetail(key: string, value: number | null): void {
      if (!(key in TYPO_DETAILS)) return;
      const coerced = value === null ? null : coerceSize(value, DETAIL_SIZE_RANGE);
      if (coerced === null) delete this.details[key];
      else this.details[key] = coerced;
      this.commit();
    },
    /** Begin pinning a detail: seed with the current computed value so the
     *  rendering doesn't jump on the first edit (theme 定制 pattern). */
    pinDetail(key: string): void {
      if (!(key in TYPO_DETAILS)) return;
      this.details[key] = this.detailSize(key);
      this.commit();
    },
    resetAll(): void {
      this.ui = null;
      this.content = null;
      this.details = {};
      this.commit();
    },
    /** Persist + apply; every mutation funnels through here. */
    commit(): void {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ ui: this.ui, content: this.content, details: this.details }));
      this.apply();
    },
    apply(): void {
      const element = targetElement();
      if (element === null) return;
      element.style.setProperty("--font-size-ui", `${this.uiSize}px`);
      element.style.setProperty("--markdown-body-font-size", `${this.contentSize}px`);
      for (const [key, spec] of Object.entries(TYPO_DETAILS)) {
        const pinned = this.details[key];
        if (pinned === undefined) element.style.removeProperty(spec.cssVar);
        else element.style.setProperty(spec.cssVar, `${pinned}px`);
      }
    },
    /** Apply once at startup (after mount, so .app-shell exists). */
    init(): void {
      this.apply();
    },
  },
});
