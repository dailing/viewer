import { onMounted, onUnmounted, watch, type Ref } from "vue";
import { saveScrollPosition } from "../utils/scrollMemory";
import type { ScrollMemoryTarget } from "../utils/scrollMemory";

export function useReloadingScrollMemory(
  path: () => string,
  version: () => unknown,
  element: Ref<HTMLElement | null>,
  load: () => void | Promise<void>,
  scope?: () => Pick<ScrollMemoryTarget, "paneId" | "workspaceId">,
): { saveCurrentScroll: () => void } {
  let skipUnmountSave = false;

  function targetFor(targetPath: string): ScrollMemoryTarget {
    return { path: targetPath, ...scope?.() };
  }

  function saveCurrentScroll() {
    saveScrollPosition(targetFor(path()), element.value);
  }

  function saveBeforePaneNavigate(event: Event) {
    const paneId = scope?.().paneId;
    if (!paneId) return;
    const targetPaneId = (event as CustomEvent<{ paneId?: string }>).detail?.paneId;
    if (targetPaneId === paneId) {
      saveCurrentScroll();
      skipUnmountSave = true;
    }
  }

  onMounted(() => {
    window.addEventListener("beforeunload", saveCurrentScroll);
    window.addEventListener("viewer:pane-before-navigate", saveBeforePaneNavigate);
    void load();
  });

  onUnmounted(() => {
    if (!skipUnmountSave) saveCurrentScroll();
    window.removeEventListener("beforeunload", saveCurrentScroll);
    window.removeEventListener("viewer:pane-before-navigate", saveBeforePaneNavigate);
  });

  watch(
    () => [path(), version()] as const,
    ([newPath], [oldPath, oldVersion]) => {
      if (oldPath && newPath !== oldPath) {
        saveScrollPosition(targetFor(oldPath), element.value);
      } else if (oldVersion !== undefined) {
        saveScrollPosition(targetFor(newPath), element.value);
      }
      skipUnmountSave = false;
      void load();
    },
  );

  return { saveCurrentScroll };
}
