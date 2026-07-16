<script setup lang="ts">
import type { LayoutNode } from "../types/layout";
import { useLayoutStore } from "../stores/layout";
import ViewerPane from "./ViewerPane.vue";

const props = defineProps<{ node: LayoutNode; loading?: boolean; workspaceId: string }>();
const layout = useLayoutStore();

function startDrag(event: PointerEvent) {
  if (props.node.type === "pane") return;
  const container = (event.currentTarget as HTMLElement).closest(".split-node") as HTMLElement | null;
  if (!container) return;

  event.preventDefault();
  const pointerId = event.pointerId;
  (event.currentTarget as HTMLElement).setPointerCapture(pointerId);

  const move = (moveEvent: PointerEvent) => {
    const rect = container.getBoundingClientRect();
    const ratio =
      props.node.type !== "pane" && props.node.direction === "horizontal"
        ? (moveEvent.clientY - rect.top) / rect.height
        : (moveEvent.clientX - rect.left) / rect.width;
    layout.setRatio(props.node.id, ratio);
  };

  const stop = () => {
    window.removeEventListener("pointermove", move);
    window.removeEventListener("pointerup", stop);
    window.removeEventListener("pointercancel", stop);
  };

  window.addEventListener("pointermove", move);
  window.addEventListener("pointerup", stop);
  window.addEventListener("pointercancel", stop);
}
</script>

<template>
  <ViewerPane v-if="node.type === 'pane'" :pane="node" :workspace-loading="loading" :workspace-id="workspaceId" />
  <div v-else class="split-node" :class="node.direction">
    <div class="split-child" :style="{ flexBasis: `${node.ratio * 100}%` }">
      <SplitNode :node="node.first" :loading="loading" :workspace-id="workspaceId" />
    </div>
    <div class="split-resizer" role="separator" title="Drag to resize" @pointerdown="startDrag"></div>
    <div class="split-child" :style="{ flexBasis: `${(1 - node.ratio) * 100}%` }">
      <SplitNode :node="node.second" :loading="loading" :workspace-id="workspaceId" />
    </div>
  </div>
</template>

<style scoped>
.split-node {
  display: flex;
  height: 100%;
  min-height: 0;
  min-width: 0;
  user-select: none;
  width: 100%;
}

.split-node.horizontal {
  flex-direction: column;
}

.split-child {
  flex-grow: 1;
  min-height: 0;
  min-width: 0;
}

.split-resizer {
  background: var(--color-surface-muted);
  flex: 0 0 3px;
  position: relative;
  touch-action: none;
}

.split-resizer::before {
  content: none;
}

.split-node.horizontal > .split-resizer {
  cursor: row-resize;
  min-height: 3px;
}

.split-node:not(.horizontal) > .split-resizer {
  cursor: col-resize;
  min-width: 3px;
}

.split-resizer:hover {
  background: var(--color-accent-soft);
}

@media (max-width: 767.98px) {
  .split-resizer {
    flex-basis: 3px;
  }

  .split-node.horizontal > .split-resizer {
    min-height: 3px;
  }

  .split-node:not(.horizontal) > .split-resizer {
    min-width: 3px;
  }

}
</style>
