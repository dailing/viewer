<script setup lang="ts">
/**
 * Recursive split renderer with a draggable 1px divider (ported from the
 * original viewer's SplitNode.vue).
 */
import type { LayoutNode } from "../stores/layout";
import { useLayoutStore } from "../stores/layout";
import PaneContainer from "./PaneContainer.vue";

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
  <PaneContainer v-if="node.type === 'pane'" :pane="node" />
  <div v-else class="split-node" :class="node.direction">
    <div class="split-child" :style="{ flexBasis: `${node.ratio * 100}%` }">
      <SplitNode :node="node.first" />
    </div>
    <div class="split-resizer" role="separator" title="拖动调整大小" @pointerdown="startDrag"></div>
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
  flex: 0 0 1px;
  position: relative;
  touch-action: none;
  z-index: 2;
}

.split-resizer::before {
  background: var(--color-border);
  content: "";
  pointer-events: none;
  position: absolute;
}

.split-resizer::after {
  background: transparent;
  content: "";
  position: absolute;
  touch-action: none;
}

.split-node.horizontal > .split-resizer {
  cursor: row-resize;
  min-height: 1px;
}

.split-node.horizontal > .split-resizer::before {
  height: 1px;
  inset: 0;
}

.split-node.horizontal > .split-resizer::after {
  inset: -4px 0;
}

.split-node:not(.horizontal) > .split-resizer {
  cursor: col-resize;
  min-width: 1px;
}

.split-node:not(.horizontal) > .split-resizer::before {
  inset: 0;
  width: 1px;
}

.split-node:not(.horizontal) > .split-resizer::after {
  inset: 0 -4px;
}

.split-resizer:hover::before,
.split-resizer:active::before {
  background: var(--color-border-strong);
}
</style>
