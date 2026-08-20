/**
 * Files instance registry: one record per virtual instance (a files pane),
 * persisted to a single localStorage key so the Dock can restore pinned
 * instances across reloads and panes can restore their view state.
 *
 * Unpinned instances are ephemeral: when no pane hosts them anymore (pane
 * closed, page reloaded), `pruneUnpinned` drops them. Pinned instances stay
 * in the Dock and reopen with the same state.
 */

import { reactive } from "vue";

export type PreviewMode = "render" | "source";

export interface FilesViewState {
  /** Current browser directory ("" = file-service root until first list). */
  dir: string;
  /** Open file path; null = nothing previewed yet. */
  file: string | null;
  mode: PreviewMode;
  /** Whether the in-panel file-list overlay is visible. */
  overlayOpen: boolean;
}

export interface FilesInstance {
  id: string;
  pinned: boolean;
  /** Dock label: open file name, else current directory name. */
  label: string;
  state: FilesViewState;
}

const STORAGE_KEY = "viewer.files.instances.v1";

function defaultState(): FilesViewState {
  return { dir: "", file: null, mode: "render", overlayOpen: true };
}

function load(): FilesInstance[] {
  const raw = localStorage.getItem(STORAGE_KEY);
  if (raw === null) return [];
  try {
    const parsed = JSON.parse(raw) as Array<Partial<FilesInstance>>;
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((entry): entry is Partial<FilesInstance> & { id: string } => typeof entry.id === "string")
      .map((entry) => ({
        id: entry.id,
        pinned: entry.pinned === true,
        label: typeof entry.label === "string" ? entry.label : "文件",
        state: { ...defaultState(), ...(entry.state ?? {}) },
      }));
  } catch {
    return [];
  }
}

export const instances = reactive<FilesInstance[]>(load());

function persist(): void {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(instances));
}

export function getInstance(id: string): FilesInstance | undefined {
  return instances.find((entry) => entry.id === id);
}

export function createInstance(): FilesInstance {
  const instance: FilesInstance = {
    id: crypto.randomUUID(),
    pinned: false,
    label: "文件",
    state: defaultState(),
  };
  instances.push(instance);
  persist();
  return instance;
}

export function removeInstance(id: string): void {
  const index = instances.findIndex((entry) => entry.id === id);
  if (index < 0) return;
  instances.splice(index, 1);
  persist();
}

export function setPinned(id: string, pinned: boolean): void {
  const instance = getInstance(id);
  if (instance === undefined) return;
  instance.pinned = pinned;
  persist();
}

export function updateState(id: string, patch: Partial<FilesViewState>, label?: string): void {
  const instance = getInstance(id);
  if (instance === undefined) return;
  Object.assign(instance.state, patch);
  if (label !== undefined) instance.label = label;
  persist();
}

/** Drop unpinned instances that no pane currently hosts. */
export function pruneUnpinned(openIds: Set<string>): void {
  for (let index = instances.length - 1; index >= 0; index -= 1) {
    const instance = instances[index];
    if (!instance.pinned && !openIds.has(instance.id)) instances.splice(index, 1);
  }
  persist();
}
