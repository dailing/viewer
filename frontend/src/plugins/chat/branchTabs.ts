/** Browser-local branch-tab persistence, isolated from the chat timeline. */
export const BRANCH_TABS_STORAGE_KEY = "viewer.chatBranchTabs.v1";
export const WINDOW_ID_STORAGE_KEY = "viewer.windowId.v1";

export interface KeyValueStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export function ensureWindowId(storage: KeyValueStorage, randomId: () => string): string {
  let id = storage.getItem(WINDOW_ID_STORAGE_KEY);
  if (id === null || id === "") {
    id = randomId();
    storage.setItem(WINDOW_ID_STORAGE_KEY, id);
  }
  return id;
}

function readMap(storage: KeyValueStorage): Record<string, unknown> {
  const raw = storage.getItem(BRANCH_TABS_STORAGE_KEY);
  if (raw === null) return {};
  const parsed = JSON.parse(raw) as unknown;
  return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)
    ? parsed as Record<string, unknown>
    : {};
}

export function loadBranchTabs(storage: KeyValueStorage, key: string): string[] {
  try {
    const tabs = readMap(storage)[key];
    if (!Array.isArray(tabs)) return [];
    return tabs.filter((tab): tab is string => typeof tab === "string" && tab !== "");
  } catch {
    return [];
  }
}

export function writeBranchTabs(storage: KeyValueStorage, key: string, legacyKey: string, tabs: string[]): void {
  try {
    const parsed = readMap(storage);
    const map = Object.fromEntries(Object.entries(parsed).filter(([, value]) => Array.isArray(value))) as Record<string, string[]>;
    if (tabs.length === 1 && tabs[0] === "main") delete map[key];
    else map[key] = [...tabs];
    if (key !== legacyKey) delete map[legacyKey];
    storage.setItem(BRANCH_TABS_STORAGE_KEY, JSON.stringify(map));
  } catch {
    // Quota/private-mode failures are non-fatal: tabs become session-local.
  }
}
