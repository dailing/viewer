<script setup lang="ts">
/**
 * Borderless tiled pane body. WorkspaceBar owns the active pane's shared
 * chrome; clicking a tile focuses it for Dock opens and bar actions.
 */
import { contentUid, type PaneNode } from "../stores/layout";
import { useLayoutStore } from "../stores/layout";
import PluginPaneHost from "./PluginPaneHost.vue";

const props = defineProps<{ pane: PaneNode }>();
const layout = useLayoutStore();
const uid = () => (props.pane.content === null ? "" : contentUid(props.pane.content));
</script>

<template>
  <div
    class="pane-container"
    :class="{ active: layout.activePaneId === pane.id }"
    @pointerdown="layout.setActivePane(pane.id)"
  >
    <div class="pane-body">
      <PluginPaneHost
        v-if="pane.content !== null"
        :key="`${uid()}:${pane.epoch}`"
        :pane-type="pane.content.paneType"
        :instance-id="pane.content.instanceId"
      />
      <div v-else class="pane-placeholder">
        <i class="bi bi-plus-square-dotted"></i>
        <div>从左侧 Dock 打开一个 instance，或点 + 新建</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pane-container {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
  width: 100%;
}

.pane-container.active {
  border-color: var(--color-accent);
  box-shadow: inset 0 0 0 1px var(--color-accent);
  position: relative;
  z-index: 1;
}

.pane-body {
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
}

.pane-placeholder {
  align-items: center;
  color: var(--color-text-subtle);
  display: flex;
  flex-direction: column;
  font-size: var(--font-size-ui-small);
  gap: 6px;
  height: 100%;
  justify-content: center;
}

.pane-placeholder > i {
  font-size: 20px;
}
</style>
