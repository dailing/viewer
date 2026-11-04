<script setup lang="ts">
/** Mount one plugin-owned settings component with a short-lived settings
 * scope. Subscriptions therefore end when the manager selects another
 * plugin, independently of the plugin's activation lifetime. */
import { onBeforeUnmount, provide, type Component } from "vue";

import { createCtx } from "../../shell/ctx";

const props = defineProps<{
  pluginId: string;
  component: Component;
}>();

const ctx = createCtx(`${props.pluginId}:settings`, "settings");
provide("pluginCtx", ctx);
onBeforeUnmount(() => ctx.dispose());
</script>

<template>
  <component :is="component" />
</template>
