<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { rawUrl } from "../../api/client";

const props = defineProps<{ path: string; contentHash: string }>();
const src = computed(() => rawUrl(props.path, props.contentHash));
const container = ref<HTMLElement | null>(null);
const scale = ref(1);
const offsetX = ref(0);
const offsetY = ref(0);
const dragging = ref(false);
const pointers = new Map<number, { x: number; y: number }>();

const MIN_SCALE = 1;
const MAX_SCALE = 12;
const ZOOM_STEP = 1.1;
const DRAG_THRESHOLD = 1;

let dragStartX = 0;
let dragStartY = 0;
let dragOriginX = 0;
let dragOriginY = 0;
let pinchStartDistance = 0;
let pinchStartScale = 1;
let pinchStartMidX = 0;
let pinchStartMidY = 0;
let pinchStartOffsetX = 0;
let pinchStartOffsetY = 0;

const transformStyle = computed(() => ({
  transform: `translate(${offsetX.value}px, ${offsetY.value}px) scale(${scale.value})`,
}));

function clampScale(value: number): number {
  return Math.max(MIN_SCALE, Math.min(MAX_SCALE, value));
}

function updateScaleAtPoint(nextScaleRaw: number, clientX: number, clientY: number): void {
  const root = container.value;
  if (!root) {
    return;
  }
  const rect = root.getBoundingClientRect();
  const px = clientX - rect.left;
  const py = clientY - rect.top;
  const currentScale = scale.value;
  const nextScale = clampScale(nextScaleRaw);
  if (Math.abs(nextScale - currentScale) < 1e-6) {
    return;
  }
  const worldX = (px - offsetX.value) / currentScale;
  const worldY = (py - offsetY.value) / currentScale;
  scale.value = nextScale;
  offsetX.value = px - worldX * nextScale;
  offsetY.value = py - worldY * nextScale;
}

watch(() => [props.path, props.contentHash] as const, async ([newPath], [oldPath, oldHash]) => {
  if (!oldPath || newPath !== oldPath || oldHash !== undefined) {
    scale.value = 1;
    offsetX.value = 0;
    offsetY.value = 0;
    pointers.clear();
    dragging.value = false;
  }
});

function handleWheel(event: WheelEvent): void {
  event.preventDefault();
  const factor = event.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP;
  updateScaleAtPoint(scale.value * factor, event.clientX, event.clientY);
}

function pointerPosition(event: PointerEvent): { x: number; y: number } {
  const root = container.value;
  if (!root) {
    return { x: 0, y: 0 };
  }
  const rect = root.getBoundingClientRect();
  return { x: event.clientX - rect.left, y: event.clientY - rect.top };
}

function beginPinch(): void {
  const points = Array.from(pointers.values());
  if (points.length !== 2) {
    return;
  }
  const [a, b] = points;
  pinchStartDistance = Math.hypot(b.x - a.x, b.y - a.y);
  if (pinchStartDistance < DRAG_THRESHOLD) {
    return;
  }
  pinchStartScale = scale.value;
  pinchStartMidX = (a.x + b.x) * 0.5;
  pinchStartMidY = (a.y + b.y) * 0.5;
  pinchStartOffsetX = offsetX.value;
  pinchStartOffsetY = offsetY.value;
  dragging.value = false;
}

function handlePointerDown(event: PointerEvent): void {
  const root = container.value;
  if (!root) {
    return;
  }
  root.setPointerCapture(event.pointerId);
  const point = pointerPosition(event);
  pointers.set(event.pointerId, point);

  if (pointers.size === 1) {
    dragStartX = point.x;
    dragStartY = point.y;
    dragOriginX = offsetX.value;
    dragOriginY = offsetY.value;
    dragging.value = true;
    return;
  }
  if (pointers.size === 2) {
    beginPinch();
  }
}

function handlePointerMove(event: PointerEvent): void {
  if (!pointers.has(event.pointerId)) {
    return;
  }
  const point = pointerPosition(event);
  pointers.set(event.pointerId, point);

  if (pointers.size === 1 && dragging.value) {
    offsetX.value = dragOriginX + (point.x - dragStartX);
    offsetY.value = dragOriginY + (point.y - dragStartY);
    return;
  }
  if (pointers.size !== 2 || pinchStartDistance < DRAG_THRESHOLD) {
    return;
  }
  const points = Array.from(pointers.values());
  const [a, b] = points;
  const distance = Math.hypot(b.x - a.x, b.y - a.y);
  const midX = (a.x + b.x) * 0.5;
  const midY = (a.y + b.y) * 0.5;
  const unclampedScale = pinchStartScale * (distance / pinchStartDistance);
  const nextScale = clampScale(unclampedScale);

  const worldX = (pinchStartMidX - pinchStartOffsetX) / pinchStartScale;
  const worldY = (pinchStartMidY - pinchStartOffsetY) / pinchStartScale;
  scale.value = nextScale;
  offsetX.value = midX - worldX * nextScale;
  offsetY.value = midY - worldY * nextScale;
}

function releasePointer(event: PointerEvent): void {
  pointers.delete(event.pointerId);
  if (pointers.size === 1) {
    const [remaining] = Array.from(pointers.values());
    dragStartX = remaining.x;
    dragStartY = remaining.y;
    dragOriginX = offsetX.value;
    dragOriginY = offsetY.value;
    dragging.value = true;
    return;
  }
  dragging.value = false;
}
</script>

<template>
  <div
    ref="container"
    class="image-viewer"
    @wheel.prevent="handleWheel"
    @pointerdown="handlePointerDown"
    @pointermove="handlePointerMove"
    @pointerup="releasePointer"
    @pointercancel="releasePointer"
  >
    <img :src="src" :alt="path" :style="transformStyle" draggable="false" />
  </div>
</template>

<style scoped>
.image-viewer {
  align-items: center;
  touch-action: none;
  background: #f8fafc;
  display: flex;
  height: 100%;
  justify-content: center;
  overflow: hidden;
  padding: 12px;
}

img {
  cursor: grab;
  max-height: 100%;
  max-width: 100%;
  object-fit: contain;
  transform-origin: 0 0;
  user-select: none;
}

.image-viewer:active img {
  cursor: grabbing;
}
</style>
