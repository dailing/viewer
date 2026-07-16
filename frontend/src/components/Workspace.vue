<script setup lang="ts">
import SplitNode from "./SplitNode.vue";
import { useLayoutStore } from "../stores/layout";

defineProps<{ loading?: boolean; workspaceId: string }>();
const layout = useLayoutStore();
</script>

<template>
  <div class="workspace">
    <SplitNode :node="layout.root" :loading="loading" :workspace-id="workspaceId" />
  </div>
</template>

<style scoped>
.workspace {
  background: var(--color-surface);
  height: 100%;
  min-height: 0;
  min-width: 0;
  padding: 0;
}

@media (max-width: 767.98px) {
  .workspace {
    padding: 0;
  }
}
</style>
