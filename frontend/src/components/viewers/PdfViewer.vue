<script setup lang="ts">
import { computed } from "vue";
import { rawUrl } from "../../api/client";

const props = defineProps<{ path: string; version: number }>();
const src = computed(() => `${rawUrl(props.path)}&v=${props.version}`);
</script>

<template>
  <iframe class="pdf-viewer" :src="src" :title="path"></iframe>
</template>

<style scoped>
.pdf-viewer {
  border: 0;
  height: 100%;
  width: 100%;
}
</style>

