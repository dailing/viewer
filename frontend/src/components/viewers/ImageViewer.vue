<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { rawUrl } from "../../api/client";

const props = defineProps<{ path: string; contentHash: string }>();
const src = computed(() => rawUrl(props.path, props.contentHash));
const container = ref<HTMLElement | null>(null);
const naturalWidth = ref(0);
const naturalHeight = ref(0);
const viewportWidth = ref(0);
const viewportHeight = ref(0);
const scale = ref(1);
const offsetX = ref(0);
const offsetY = ref(0);
const dragging = ref(false);
const pointers = new Map<number, { x: number; y: number }>();

const MIN_SCALE = 1;
const MAX_SCALE = 12;
const ZOOM_STEP = 1.1;
const DRAG_THRESHOLD = 1;
const VIEWER_PADDING = 24;

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
let resizeObserver: ResizeObserver | null = null;

const baseSize = computed(() => {
  const width = naturalWidth.value;
  const height = naturalHeight.value;
  if (width <= 0 || height <= 0) {
    return { width: 0, height: 0 };
  }

  const availableWidth = Math.max(1, viewportWidth.value - VIEWER_PADDING);
  const availableHeight = Math.max(1, viewportHeight.value - VIEWER_PADDING);
  const fitScale = Math.min(1, availableWidth / width, availableHeight / height);
  return {
    width: width * fitScale,
    height: height * fitScale,
  };
});

const displaySize = computed(() => ({
  width: baseSize.value.width * scale.value,
  height: baseSize.value.height * scale.value,
}));

const imageStyle = computed(() => ({
  height: `${displaySize.value.height}px`,
  transform: `translate(${offsetX.value}px, ${offsetY.value}px)`,
  width: `${displaySize.value.width}px`,
}));

function clampScale(value: number): number {
  return Math.max(MIN_SCALE, Math.min(MAX_SCALE, value));
}

function imageLeftForScale(value: number): number {
  return (viewportWidth.value - baseSize.value.width * value) * 0.5;
}

function imageTopForScale(value: number): number {
  return (viewportHeight.value - baseSize.value.height * value) * 0.5;
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
  const worldX = (px - imageLeftForScale(currentScale) - offsetX.value) / currentScale;
  const worldY = (py - imageTopForScale(currentScale) - offsetY.value) / currentScale;
  scale.value = nextScale;
  offsetX.value = px - imageLeftForScale(nextScale) - worldX * nextScale;
  offsetY.value = py - imageTopForScale(nextScale) - worldY * nextScale;
}

function updateViewportSize(): void {
  const root = container.value;
  if (!root) {
    viewportWidth.value = 0;
    viewportHeight.value = 0;
    return;
  }
  const rect = root.getBoundingClientRect();
  viewportWidth.value = rect.width;
  viewportHeight.value = rect.height;
}

function handleImageLoad(event: Event): void {
  const image = event.target as HTMLImageElement;
  naturalWidth.value = image.naturalWidth;
  naturalHeight.value = image.naturalHeight;
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

  const worldX = (pinchStartMidX - imageLeftForScale(pinchStartScale) - pinchStartOffsetX) / pinchStartScale;
  const worldY = (pinchStartMidY - imageTopForScale(pinchStartScale) - pinchStartOffsetY) / pinchStartScale;
  scale.value = nextScale;
  offsetX.value = midX - imageLeftForScale(nextScale) - worldX * nextScale;
  offsetY.value = midY - imageTopForScale(nextScale) - worldY * nextScale;
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

onMounted(() => {
  updateViewportSize();
  if (container.value) {
    resizeObserver = new ResizeObserver(updateViewportSize);
    resizeObserver.observe(container.value);
  }
});

onUnmounted(() => {
  resizeObserver?.disconnect();
  resizeObserver = null;
});
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
    <img :src="src" :alt="path" :style="imageStyle" draggable="false" @load="handleImageLoad" />
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
  object-fit: contain;
  transform-origin: center;
  user-select: none;
}

.image-viewer:active img {
  cursor: grabbing;
}
</style>
