<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

import type { LayoutMode, PaneNode } from "../stores/layout";
import PaneContainer from "./PaneContainer.vue";

const props = defineProps<{ panes: PaneNode[]; mode: Exclude<LayoutMode, "free"> }>();

const root = ref<HTMLElement | null>(null);
const wide = ref(true);
let observer: ResizeObserver | null = null;

onMounted(() => {
  observer = new ResizeObserver(([entry]) => {
    if (entry !== undefined) wide.value = entry.contentRect.width > entry.contentRect.height;
  });
  if (root.value !== null) observer.observe(root.value);
});

onBeforeUnmount(() => observer?.disconnect());

const first = computed(() => props.panes[0]);
const rest = computed(() => props.panes.slice(1));
</script>

<template>
  <div
    v-if="mode === 'cascade'"
    ref="root"
    class="cascade-node"
    :class="wide ? 'wide' : 'tall'"
  >
    <PaneContainer v-if="first" class="cascade-first" :pane="first" />
    <TilingLayout v-if="rest.length" class="cascade-rest" :panes="rest" mode="cascade" />
  </div>
  <div v-else class="equal-layout" :class="mode">
    <PaneContainer v-for="pane in panes" :key="pane.id" :pane="pane" />
  </div>
</template>

<style scoped>
.cascade-node,
.equal-layout {
  display: flex;
  height: 100%;
  min-height: 0;
  min-width: 0;
  width: 100%;
}

.cascade-node.wide,
.equal-layout.columns {
  flex-direction: row;
}

.cascade-node.tall,
.equal-layout.rows {
  flex-direction: column;
}

.cascade-first,
.cascade-rest,
.equal-layout > :deep(*) {
  flex: 1 1 0;
  min-height: 0;
  min-width: 0;
}
</style>
