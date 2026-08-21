<script setup lang="ts">
/**
 * Floating layer: overlay above the split tree hosting every floating pane.
 * Transparent to pointer events except on the panes themselves, so the tree
 * below stays fully interactive.
 */
import FloatingPane from "./FloatingPane.vue";
import { useLayoutStore } from "../stores/layout";

const layout = useLayoutStore();
</script>

<template>
  <div v-if="layout.floating.length > 0" class="floating-layer">
    <FloatingPane v-for="pane in layout.floating" :key="pane.id" :pane="pane" />
  </div>
</template>

<style scoped>
.floating-layer {
  inset: 0;
  overflow: hidden;
  pointer-events: none;
  position: absolute;
}
</style>
