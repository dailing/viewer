<script setup lang="ts">
import type { LayoutNode } from "../types/layout";
import { useLayoutStore } from "../stores/layout";
import ViewerPane from "./ViewerPane.vue";

const props = defineProps<{ node: LayoutNode }>();
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
  <ViewerPane v-if="node.type === 'pane'" :pane="node" />
  <div v-else class="split-node" :class="node.direction">
    <div class="split-child" :style="{ flexBasis: `${node.ratio * 100}%` }">
      <SplitNode :node="node.first" />
    </div>
    <div class="split-resizer" role="separator" title="Drag to resize" @pointerdown="startDrag"></div>
    <div class="split-child" :style="{ flexBasis: `${(1 - node.ratio) * 100}%` }">
      <SplitNode :node="node.second" />
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
  background: transparent;
  flex: 0 0 7px;
  position: relative;
  touch-action: none;
}

.split-resizer::before {
  background: var(--border);
  content: "";
  inset: 0 3px;
  position: absolute;
}

.split-node.horizontal > .split-resizer {
  cursor: row-resize;
  min-height: 7px;
}

.split-node:not(.horizontal) > .split-resizer {
  cursor: col-resize;
  min-width: 7px;
}

.split-node.horizontal > .split-resizer::before {
  inset: 3px 0;
}

.split-resizer:hover::before {
  background: #4f6f96;
}
</style>
