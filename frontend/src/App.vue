<script setup lang="ts">
/**
 * Shell layout: Dock on the left, shared active-pane bar and tiled/floating
 * workspace on the right.
 */
import Dock from "./shell/Dock.vue";
import FloatingLayer from "./shell/FloatingLayer.vue";
import SplitNode from "./shell/SplitNode.vue";
import TilingLayout from "./shell/TilingLayout.vue";
import WorkspaceBar from "./shell/WorkspaceBar.vue";
import { useLayoutStore } from "./stores/layout";

const layout = useLayoutStore();
</script>

<template>
  <div class="app-shell">
    <Dock />
    <main class="workspace-shell">
      <WorkspaceBar />
      <div class="workspace">
        <SplitNode v-if="layout.mode === 'free'" :node="layout.root" />
        <TilingLayout v-else :panes="layout.panes" :mode="layout.mode" />
        <FloatingLayer />
      </div>
    </main>
  </div>
</template>

<style scoped>
.workspace-shell {
  background: var(--color-surface);
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
}

.workspace {
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  overscroll-behavior: none;
  overflow: hidden;
  position: relative;
}
</style>
