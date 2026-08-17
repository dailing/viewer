/**
 * Dock display settings: browser-local toggles for the shell Dock, persisted
 * to localStorage (non-namespaced, like the layout and chat-settings stores).
 * Shared between the Dock itself (hover expand behavior) and the settings
 * pane (editing UI).
 *
 * `hoverExpandMs`: pointer-enter delay before the Dock expands to show
 * labels; 0 expands immediately.
 */
import { defineStore } from "pinia";

const STORAGE_KEY = "viewer.dock.hoverExpandMs.v1";
const DEFAULT_HOVER_EXPAND_MS = 500;

function loadHoverExpandMs(): number {
  const stored = Number(localStorage.getItem(STORAGE_KEY));
  return Number.isFinite(stored) && stored >= 0 ? stored : DEFAULT_HOVER_EXPAND_MS;
}

export const useDockSettingsStore = defineStore("dockSettings", {
  state: () => ({ hoverExpandMs: loadHoverExpandMs() }),
  actions: {
    setHoverExpandMs(value: number): void {
      this.hoverExpandMs = Number.isFinite(value) && value >= 0 ? value : DEFAULT_HOVER_EXPAND_MS;
      localStorage.setItem(STORAGE_KEY, String(this.hoverExpandMs));
    },
  },
});
