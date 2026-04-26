<script setup lang="ts">
import SplitNode from "./SplitNode.vue";
import { useLayoutStore } from "../stores/layout";

const layout = useLayoutStore();
</script>

<template>
  <div class="workspace">
    <SplitNode :node="layout.root" />
  </div>
</template>

<style scoped>
.workspace {
  height: 100%;
  min-height: 0;
  min-width: 0;
  padding: 8px;
}
</style>

