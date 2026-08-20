/**
 * Markdown style customization (framework v0.30): user overrides for the
 * `--markdown-*` / `--syntax-*` CSS variables that style .markdown-content.
 * Defaults live in styles.css (light + dark); this store only layers explicit
 * user choices on top, persisted to localStorage.
 */
import { defineStore } from "pinia";

const STORAGE_KEY = "viewer.markdownTheme.v1";

export interface MarkdownStyleOverrides {
  bodyFontSize?: number;
  bodyLineHeight?: number;
  bodyColor?: string;
  strongColor?: string;
  linkColor?: string;
  codeFontSize?: number;
  codeColor?: string;
  codeBackground?: string;
  syntaxText?: string;
  syntaxBackground?: string;
  borderColor?: string;
}

/** override field -> CSS variables it drives (values converted per field) */
const FIELD_VARS: Record<keyof MarkdownStyleOverrides, string[]> = {
  bodyFontSize: ["--markdown-body-font-size", "--markdown-paragraph-font-size"],
  bodyLineHeight: ["--markdown-body-line-height", "--markdown-paragraph-line-height"],
  bodyColor: ["--markdown-body-color", "--markdown-paragraph-color"],
  strongColor: ["--markdown-strong-color"],
  linkColor: ["--markdown-link-color"],
  codeFontSize: ["--markdown-code-font-size"],
  codeColor: ["--markdown-code-color"],
  codeBackground: ["--markdown-code-background"],
  syntaxText: ["--syntax-text"],
  syntaxBackground: ["--syntax-background"],
  borderColor: ["--markdown-border-color"],
};

function toCssValue(field: keyof MarkdownStyleOverrides, value: number | string): string {
  if (field === "bodyFontSize" || field === "codeFontSize") return `${value}px`;
  return String(value);
}

function targetElement(): HTMLElement {
  return document.querySelector<HTMLElement>(".app-shell") ?? document.documentElement;
}

function applyOverrides(overrides: MarkdownStyleOverrides): void {
  const element = targetElement();
  for (const [field, vars] of Object.entries(FIELD_VARS) as Array<[keyof MarkdownStyleOverrides, string[]]>) {
    const value = overrides[field];
    for (const name of vars) {
      if (value === undefined) element.style.removeProperty(name);
      else element.style.setProperty(name, toCssValue(field, value));
    }
  }
}

function loadOverrides(): MarkdownStyleOverrides {
  const raw = localStorage.getItem(STORAGE_KEY);
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw) as MarkdownStyleOverrides;
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
}

export const useMarkdownStyleStore = defineStore("markdownStyle", {
  state: () => ({ overrides: loadOverrides() as MarkdownStyleOverrides }),
  actions: {
    set<K extends keyof MarkdownStyleOverrides>(field: K, value: MarkdownStyleOverrides[K] | undefined): void {
      if (value === undefined) delete this.overrides[field];
      else this.overrides[field] = value;
      this.persist();
      applyOverrides(this.overrides);
    },
    reset(): void {
      this.overrides = {};
      this.persist();
      applyOverrides(this.overrides);
    },
    /** Apply persisted overrides; call once at app startup. */
    init(): void {
      applyOverrides(this.overrides);
    },
    persist(): void {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(this.overrides));
    },
  },
});
