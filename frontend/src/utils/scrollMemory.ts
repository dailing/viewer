import { nextTick } from "vue";
import { storageKey } from "./storage";

const STORAGE_KEY = "viewer.scrollPositions.v2";

interface ScrollPosition {
  top: number;
  left: number;
}

export type ScrollMemoryTarget = {
  path: string;
  paneId?: string;
  workspaceId?: string;
};

function readAll(): Record<string, ScrollPosition> {
  try {
    return JSON.parse(localStorage.getItem(storageKey(STORAGE_KEY)) || "{}") as Record<string, ScrollPosition>;
  } catch {
    return {};
  }
}

function writeAll(value: Record<string, ScrollPosition>): void {
  localStorage.setItem(storageKey(STORAGE_KEY), JSON.stringify(value));
}

function keyFor(target: string | ScrollMemoryTarget): string {
  if (typeof target === "string") return target;
  return [target.workspaceId || "workspace", target.paneId || "pane", target.path].join("\u0000");
}

export function saveScrollPosition(target: string | ScrollMemoryTarget, element: HTMLElement | null): void {
  if (!element) return;
  const all = readAll();
  all[keyFor(target)] = {
    top: element.scrollTop,
    left: element.scrollLeft,
  };
  writeAll(all);
}

export async function restoreScrollPosition(target: string | ScrollMemoryTarget, element: HTMLElement | null): Promise<void> {
  if (!element) return;
  const all = readAll();
  const position = all[keyFor(target)];
  if (!position) return;

  await nextTick();
  let attempts = 0;
  const restore = () => {
    element.scrollTo({
      top: Math.min(position.top, element.scrollHeight),
      left: Math.min(position.left, element.scrollWidth),
    });
    attempts += 1;
    if (attempts < 8) requestAnimationFrame(restore);
  };
  requestAnimationFrame(restore);
}
