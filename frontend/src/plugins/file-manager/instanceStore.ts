/** Server-synced File Manager instances. Directory, hidden-entry visibility and
 * paired viewer are instance state; the File Viewer plugin owns all preview state. */
import { reactive } from "vue";
import type { BusFrame } from "@viewer/bus-sdk";
import { bus } from "../../shell/bus";
import type { PluginCtx } from "../../shell/ctx";

export interface FileManagerState {
  dir: string;
  viewerId: string | null;
  showHidden: boolean;
}

export interface FileManagerInstance {
  id: string;
  pinned: boolean;
  label: string;
  state: FileManagerState;
}

const PLUGIN_ID = "file-manager";
const MAILBOX_PREFIX = "instance-store:file-manager:";
const defaultState = (): FileManagerState => ({ dir: "", viewerId: null, showHidden: false });
export const instances = reactive<FileManagerInstance[]>([]);

function toWire(instance: FileManagerInstance): Record<string, unknown> {
  return { pinned: instance.pinned, label: instance.label, state: { ...instance.state } };
}

function fromWire(id: string, value: unknown): FileManagerInstance | null {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return null;
  const raw = value as Record<string, unknown>;
  const state = (typeof raw.state === "object" && raw.state !== null ? raw.state : {}) as Partial<FileManagerState>;
  return {
    id,
    pinned: raw.pinned === true,
    label: typeof raw.label === "string" ? raw.label : "文件管理器",
    state: { ...defaultState(), ...state },
  };
}

function pushRemote(instance: FileManagerInstance): void {
  void bus.request("instance:_:set", { plugin: PLUGIN_ID, instance: instance.id, value: toWire(instance) }).catch(() => undefined);
}
function deleteRemote(id: string): void {
  void bus.request("instance:_:delete", { plugin: PLUGIN_ID, instance: id }).catch(() => undefined);
}
function upsertLocal(instance: FileManagerInstance): void {
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
  if (frame.value === null) removeLocal(id);
  else {
    const instance = fromWire(id, frame.value);
    if (instance !== null) upsertLocal(instance);
  }
}

let initialized = false;
let resolveFirstLoad: (() => void) | null = null;
const firstLoad = new Promise<void>((resolve) => { resolveFirstLoad = resolve; });
async function refresh(): Promise<void> {
  try {
    const result = await bus.request("instance:_:list", { plugin: PLUGIN_ID }) as Record<string, unknown> | null;
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
    // The next bus connection retries the initial reconciliation.
  }
}

export function initInstanceStore(ctx: PluginCtx): Promise<void> {
  if (initialized) return firstLoad;
  initialized = true;
  ctx.bus.subscribe(`${MAILBOX_PREFIX}*`, handleMailbox);
  bus.onStateChange((connected) => { if (connected) void refresh(); });
  void refresh();
  return firstLoad;
}

export function getInstance(id: string): FileManagerInstance | undefined {
  return instances.find((entry) => entry.id === id);
}
export function createInstance(seed?: Partial<FileManagerState>, label = "文件管理器"): FileManagerInstance {
  const instance: FileManagerInstance = { id: crypto.randomUUID(), pinned: false, label, state: { ...defaultState(), ...seed } };
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
export function updateState(id: string, patch: Partial<FileManagerState>, label?: string): void {
  let instance = getInstance(id);
  if (instance === undefined) {
    instance = { id, pinned: false, label: label ?? "文件管理器", state: defaultState() };
    instances.push(instance);
  }
  instance.state = { ...instance.state, ...patch };
  if (label !== undefined) instance.label = label;
  pushRemote(instance);
}
export function pruneUnpinned(openIds: Set<string>): void {
  for (let index = instances.length - 1; index >= 0; index -= 1) {
    const instance = instances[index];
    if (!instance.pinned && !openIds.has(instance.id)) {
      instances.splice(index, 1);
      deleteRemote(instance.id);
    }
  }
}
