<script setup lang="ts">
/**
 * Shell layout: Dock (running instances) on the left, splittable workspace
 * on the right. No top NavBar — the Dock's plug owns connection state.
 */
import Dock from "./shell/Dock.vue";
import SplitNode from "./shell/SplitNode.vue";
import { useLayoutStore } from "./stores/layout";

const layout = useLayoutStore();
</script>

<template>
  <div class="app-shell">
    <Dock />
    <main class="workspace">
      <SplitNode :node="layout.root" />
    </main>
  </div>
</template>

<style scoped>
.workspace {
  background: var(--color-surface);
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  overscroll-behavior: none;
  overflow: hidden;
}
</style>
