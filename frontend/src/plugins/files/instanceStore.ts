/**
 * Files instance registry: one record per virtual instance (a files pane).
 * Records persist server-side via the instance-store core plugin (framework
 * section 7.2 / 10.2 C2), so pinned instances survive restarts, browsers and
 * machines; the `instance-store:files:*` mailbox propagates changes live to
 * other open browsers. Local mutations apply optimistically to the reactive
 * array, then replicate with a whole-state `instance:_:set` (fire-and-forget).
 *
 * Unpinned instances are ephemeral: when no pane hosts them anymore (pane
 * closed, page reloaded), `pruneUnpinned` drops them. A hosting pane whose
 * record was pruned by another browser recreates it on its next state write
 * (upsert semantics in `updateState`), so live panes never lose their record.
 */

import { reactive } from "vue";

import type { BusFrame } from "@viewer/bus-sdk";

import { bus } from "../../shell/bus";
import type { PluginCtx } from "../../shell/ctx";

export type PreviewMode = "render" | "source";

export interface FilesViewState {
  /** Current browser directory ("" = file-service root until first list). */
  dir: string;
  /** Open file path; null = nothing previewed yet. */
  file: string | null;
  mode: PreviewMode;
  /** Whether the in-panel file-list overlay is visible. */
  overlayOpen: boolean;
  /** PDF margin crop per axis, 0–100 (100 = trim to content, 0 = full page). */
  trimX: number;
  trimY: number;
  /** PDF theme mapping: remap page luminance onto canvas/text colors. */
  themeMap: boolean;
}

export interface FilesInstance {
  id: string;
  pinned: boolean;
  /** Dock label: open file name, else current directory name. */
  label: string;
  state: FilesViewState;
}

const PLUGIN_ID = "files";
const MAILBOX_PREFIX = "instance-store:files:";

function defaultState(): FilesViewState {
  return { dir: "", file: null, mode: "render", overlayOpen: true, trimX: 100, trimY: 100, themeMap: true };
}

export const instances = reactive<FilesInstance[]>([]);

function toWire(instance: FilesInstance): Record<string, unknown> {
  return { pinned: instance.pinned, label: instance.label, state: { ...instance.state } };
}

function fromWire(id: string, value: unknown): FilesInstance | null {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return null;
  const raw = value as Record<string, unknown>;
  const state = (typeof raw.state === "object" && raw.state !== null ? raw.state : {}) as Partial<FilesViewState>;
  return {
    id,
    pinned: raw.pinned === true,
    label: typeof raw.label === "string" ? raw.label : "文件",
    state: { ...defaultState(), ...state },
  };
}

function pushRemote(instance: FilesInstance): void {
  bus
    .request("instance:_:set", { plugin: PLUGIN_ID, instance: instance.id, value: toWire(instance) })
    .catch(() => undefined);
}

function deleteRemote(id: string): void {
  bus.request("instance:_:delete", { plugin: PLUGIN_ID, instance: id }).catch(() => undefined);
}

function upsertLocal(instance: FilesInstance): void {
  const index = instances.findIndex((entry) => entry.id === instance.id);
  if (index >= 0) instances[index] = instance;
  else instances.push(instance);
}

function removeLocal(id: string): void {
  const index = instances.findIndex((entry) => entry.id === id);
  if (index >= 0) instances.splice(index, 1);
}

function handleMailbox(frame: BusFrame): void {
  if (!frame.channel.startsWith(MAILBOX_PREFIX)) return;
  const id = frame.channel.slice(MAILBOX_PREFIX.length);
  if (id === "") return;
  if (frame.value === null) {
    removeLocal(id);
    return;
  }
  const instance = fromWire(id, frame.value);
  if (instance !== null) upsertLocal(instance);
}

/** Full reconcile with the server; runs on init and on every (re)connect. */
async function refresh(): Promise<void> {
  try {
    const result = (await bus.request("instance:_:list", { plugin: PLUGIN_ID })) as Record<
      string,
      unknown
    > | null;
    if (result === null || typeof result !== "object" || Array.isArray(result)) return;
    const seen = new Set<string>();
    for (const [id, value] of Object.entries(result)) {
      const instance = fromWire(id, value);
      if (instance === null) continue;
      seen.add(id);
      upsertLocal(instance);
    }
    for (let index = instances.length - 1; index >= 0; index -= 1) {
      if (!seen.has(instances[index].id)) instances.splice(index, 1);
    }
    resolveFirstLoad?.();
    resolveFirstLoad = null;
  } catch {
    // Not connected yet (bootstrap order) or instance-store missing — the
    // next connect triggers another refresh.
  }
}

let initialized = false;
let resolveFirstLoad: (() => void) | null = null;
const firstLoad = new Promise<void>((resolve) => {
  resolveFirstLoad = resolve;
});

/**
 * Called once from the plugin's dock-provider setup: live sync + initial load.
 * Resolves after the first successful server refresh (never rejects), so the
 * caller can prune stale unpinned records once the registry is populated.
 */
export function initInstanceStore(ctx: PluginCtx): Promise<void> {
  if (initialized) return firstLoad;
  initialized = true;
  ctx.bus.subscribe(`${MAILBOX_PREFIX}*`, handleMailbox);
  bus.onStateChange((connected) => {
    if (connected) void refresh();
  });
  void refresh();
  return firstLoad;
}

export function getInstance(id: string): FilesInstance | undefined {
  return instances.find((entry) => entry.id === id);
}

export function createInstance(seed?: Partial<FilesViewState>, label?: string): FilesInstance {
  const instance: FilesInstance = {
    id: crypto.randomUUID(),
    pinned: false,
    label: label ?? "文件",
    state: { ...defaultState(), ...seed },
  };
  instances.push(instance);
  pushRemote(instance);
  return instance;
}

export function removeInstance(id: string): void {
  removeLocal(id);
  deleteRemote(id);
}

export function setPinned(id: string, pinned: boolean): void {
  const instance = getInstance(id);
  if (instance === undefined) return;
  instance.pinned = pinned;
  pushRemote(instance);
}

export function updateState(id: string, patch: Partial<FilesViewState>, label?: string): void {
  let instance = getInstance(id);
  if (instance === undefined) {
    // A live pane whose record was pruned by another browser recreates it
    // here, so hosting a pane always wins over garbage collection.
    instance = { id, pinned: false, label: label ?? "文件", state: defaultState() };
    instances.push(instance);
  }
  Object.assign(instance.state, patch);
  if (label !== undefined) instance.label = label;
  pushRemote(instance);
}

/** Drop unpinned instances that no pane currently hosts. */
export function pruneUnpinned(openIds: Set<string>): void {
  for (let index = instances.length - 1; index >= 0; index -= 1) {
    const instance = instances[index];
    if (!instance.pinned && !openIds.has(instance.id)) {
      instances.splice(index, 1);
      deleteRemote(instance.id);
    }
  }
}
