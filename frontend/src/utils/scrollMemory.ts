import { nextTick } from "vue";
import { namespacedStorageKey } from "./userProfile";

const STORAGE_KEY = "viewer.scrollPositions.v1";

interface ScrollPosition {
  top: number;
  left: number;
}

function readAll(): Record<string, ScrollPosition> {
  try {
    return JSON.parse(localStorage.getItem(namespacedStorageKey(STORAGE_KEY)) || "{}") as Record<string, ScrollPosition>;
  } catch {
    return {};
  }
}

function writeAll(value: Record<string, ScrollPosition>): void {
  localStorage.setItem(namespacedStorageKey(STORAGE_KEY), JSON.stringify(value));
}

function keyFor(path: string): string {
  return path;
}

export function saveScrollPosition(path: string, element: HTMLElement | null): void {
  if (!element) return;
  const all = readAll();
  all[keyFor(path)] = {
    top: element.scrollTop,
    left: element.scrollLeft,
  };
  writeAll(all);
}

export async function restoreScrollPosition(path: string, element: HTMLElement | null): Promise<void> {
  if (!element) return;
  const position = readAll()[keyFor(path)];
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
