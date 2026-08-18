/**
 * Dock display settings: browser-local toggles for the shell Dock, persisted
 * to localStorage (non-namespaced, like the layout and chat-settings stores).
 * Shared between the Dock itself (hover expand behavior) and the settings
 * pane (editing UI).
 *
 * `hoverExpandMs`: pointer-enter delay before the Dock expands to show
 * labels; 0 expands immediately.
 *
 * `pinned`: keep the Dock permanently expanded; the expanded width takes
 * real layout space (compressing the panes to its right) instead of
 * overlaying them on hover.
 *
 * `width`: expanded Dock width in px, adjustable by dragging the right edge
 * of the expanded Dock; applies both to the pinned layout width and to the
 * hover-overlay width.
 */
import { defineStore } from "pinia";

const STORAGE_KEY = "viewer.dock.hoverExpandMs.v1";
const PINNED_KEY = "viewer.dock.pinned.v1";
const WIDTH_KEY = "viewer.dock.width.v1";
const DEFAULT_HOVER_EXPAND_MS = 500;
export const DEFAULT_DOCK_WIDTH = 220;
export const MIN_DOCK_WIDTH = 140;
export const MAX_DOCK_WIDTH = 520;

export function clampDockWidth(value: number): number {
  if (!Number.isFinite(value)) return DEFAULT_DOCK_WIDTH;
  return Math.min(MAX_DOCK_WIDTH, Math.max(MIN_DOCK_WIDTH, Math.round(value)));
}

function loadHoverExpandMs(): number {
  const stored = Number(localStorage.getItem(STORAGE_KEY));
  return Number.isFinite(stored) && stored >= 0 ? stored : DEFAULT_HOVER_EXPAND_MS;
}

function loadPinned(): boolean {
  return localStorage.getItem(PINNED_KEY) === "true";
}

function loadWidth(): number {
  return clampDockWidth(Number(localStorage.getItem(WIDTH_KEY) ?? DEFAULT_DOCK_WIDTH));
}

export const useDockSettingsStore = defineStore("dockSettings", {
  state: () => ({ hoverExpandMs: loadHoverExpandMs(), pinned: loadPinned(), width: loadWidth() }),
  actions: {
    setHoverExpandMs(value: number): void {
      this.hoverExpandMs = Number.isFinite(value) && value >= 0 ? value : DEFAULT_HOVER_EXPAND_MS;
      localStorage.setItem(STORAGE_KEY, String(this.hoverExpandMs));
    },
    setPinned(value: boolean): void {
      this.pinned = value;
      localStorage.setItem(PINNED_KEY, String(value));
    },
    setWidth(value: number): void {
      this.width = clampDockWidth(value);
      localStorage.setItem(WIDTH_KEY, String(this.width));
    },
  },
});
