<script setup lang="ts">
/**
 * Generic pane host (framework section 8.3 step 2): resolves instance.type via
 * the component registry, shows a placeholder while the type is unknown
 * (framework section 8.6 rule 4), and disposes the pane ctx on close.
 */

import { computed, onUnmounted, provide, watch } from "vue";

import { createCtx, type PluginCtx } from "./ctx";
import { componentRegistry } from "./registries";

const props = defineProps<{
  paneType: string;
  instanceId: string;
}>();

const component = computed(() => componentRegistry.get(props.paneType));

let ctx: PluginCtx | undefined;
watch(
  () => [props.paneType, props.instanceId],
  () => {
    ctx?.dispose();
    ctx = createCtx(`${props.paneType}:${props.instanceId}`, props.instanceId);
    provide("pluginCtx", ctx);
  },
  { immediate: true },
);

onUnmounted(() => {
  ctx?.dispose();
});
</script>

<template>
  <component :is="component" v-if="component !== undefined" />
  <div v-else class="d-flex align-items-center justify-content-center h-100 text-muted">
    <div class="text-center">
      <i class="bi bi-puzzle fs-3"></i>
      <div class="mt-2">未知 pane 类型「{{ paneType }}」，等待插件注册…</div>
    </div>
  </div>
</template>
