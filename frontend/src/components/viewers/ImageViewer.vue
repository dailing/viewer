<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { rawUrl } from "../../api/client";
import { restoreScrollPosition, saveScrollPosition } from "../../utils/scrollMemory";

const props = defineProps<{ path: string; version: number }>();
const src = computed(() => `${rawUrl(props.path)}&v=${props.version}`);
const container = ref<HTMLElement | null>(null);

function persistCurrentScroll() {
  saveScrollPosition(props.path, container.value);
}

async function restore() {
  await restoreScrollPosition(props.path, container.value);
}

onMounted(() => {
  window.addEventListener("beforeunload", persistCurrentScroll);
  void restore();
});
onUnmounted(() => {
  persistCurrentScroll();
  window.removeEventListener("beforeunload", persistCurrentScroll);
});
watch(() => [props.path, props.version] as const, async ([newPath], [oldPath, oldVersion]) => {
  if (oldPath && newPath !== oldPath) {
    saveScrollPosition(oldPath, container.value);
  } else if (oldVersion !== undefined) {
    saveScrollPosition(newPath, container.value);
  }
  await restore();
});
</script>

<template>
  <div ref="container" class="image-viewer" @scroll.passive="saveScrollPosition(path, container)">
    <img :src="src" :alt="path" @load="restore" />
  </div>
</template>

<style scoped>
.image-viewer {
  align-items: center;
  background: #f8fafc;
  display: flex;
  height: 100%;
  justify-content: center;
  overflow: auto;
  padding: 12px;
}

img {
  max-height: 100%;
  max-width: 100%;
  object-fit: contain;
}
</style>
