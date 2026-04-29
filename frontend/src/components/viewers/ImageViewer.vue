<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { rawUrl } from "../../api/client";
import { restoreScrollPosition, saveScrollPosition } from "../../utils/scrollMemory";

const props = defineProps<{ path: string; contentHash: string }>();
const src = computed(() => rawUrl(props.path, props.contentHash));
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
watch(() => [props.path, props.contentHash] as const, async ([newPath], [oldPath, oldHash]) => {
  if (oldPath && newPath !== oldPath) {
    saveScrollPosition(oldPath, container.value);
  } else if (oldHash !== undefined) {
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
