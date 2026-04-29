import { onMounted, onUnmounted, watch, type Ref } from "vue";
import { saveScrollPosition } from "../utils/scrollMemory";

export function useReloadingScrollMemory(
  path: () => string,
  version: () => unknown,
  element: Ref<HTMLElement | null>,
  load: () => void | Promise<void>,
): { saveCurrentScroll: () => void } {
  function saveCurrentScroll() {
    saveScrollPosition(path(), element.value);
  }

  onMounted(() => {
    window.addEventListener("beforeunload", saveCurrentScroll);
    void load();
  });

  onUnmounted(() => {
    saveCurrentScroll();
    window.removeEventListener("beforeunload", saveCurrentScroll);
  });

  watch(
    () => [path(), version()] as const,
    ([newPath], [oldPath, oldVersion]) => {
      if (oldPath && newPath !== oldPath) {
        saveScrollPosition(oldPath, element.value);
      } else if (oldVersion !== undefined) {
        saveScrollPosition(newPath, element.value);
      }
      void load();
    },
  );

  return { saveCurrentScroll };
}
