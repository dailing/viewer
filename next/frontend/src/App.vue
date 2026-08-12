<script setup lang="ts">
import { computed } from "vue";

import { busState } from "./shell/bus";
import PluginPaneHost from "./shell/PluginPaneHost.vue";
import { sidebarTools } from "./shell/registries";
import { useShellStore } from "./stores/shell";

const shell = useShellStore();

const activePane = computed(() => shell.panes.find((pane) => pane.uid === shell.activeUid));

const connectionBadge = computed(() => (busState.connected ? "text-bg-success" : "text-bg-danger"));
const connectionText = computed(() => (busState.connected ? "已连接" : "连接中…"));

function paneLabel(pane: { paneType: string; instanceId: string }): string {
  return pane.instanceId === "main" ? pane.paneType : `${pane.paneType} ${pane.instanceId}`;
}
</script>

<template>
  <div class="d-flex vh-100">
    <nav class="d-flex flex-column align-items-center border-end py-2" style="width: 3.5rem">
      <button
        v-for="tool in sidebarTools"
        :key="tool.id"
        type="button"
        class="btn btn-sm btn-outline-secondary border-0 mb-1"
        :title="tool.title"
        @click="shell.openPane(tool.paneType)"
      >
        <i class="bi" :class="tool.icon"></i>
      </button>
      <div class="mt-auto">
        <span class="badge" :class="connectionBadge" :title="busState.conn ?? ''">
          <i class="bi" :class="busState.connected ? 'bi-plug-fill' : 'bi-plug'"></i>
        </span>
      </div>
    </nav>
    <main class="flex-grow-1 d-flex flex-column overflow-hidden">
      <header class="d-flex align-items-center border-bottom px-3 py-2 gap-2">
        <strong>Viewer</strong>
        <div v-if="shell.panes.length > 0" class="d-flex align-items-center gap-1 ms-2 overflow-auto">
          <span
            v-for="pane in shell.panes"
            :key="pane.uid"
            class="badge rounded-pill pane-tab"
            :class="pane.uid === shell.activeUid ? 'text-bg-primary' : 'text-bg-secondary'"
            role="button"
            @click="shell.setActive(pane.uid)"
          >
            {{ paneLabel(pane) }}
            <i
              class="bi bi-x ms-1"
              role="button"
              title="关闭"
              @click.stop="shell.closePane(pane.uid)"
            ></i>
          </span>
        </div>
        <span class="badge ms-auto" :class="connectionBadge">{{ connectionText }}</span>
      </header>
      <div class="flex-grow-1 overflow-hidden position-relative">
        <PluginPaneHost
          v-if="activePane !== undefined"
          :key="activePane.uid"
          :pane-type="activePane.paneType"
          :instance-id="activePane.instanceId"
        />
        <div v-else class="d-flex align-items-center justify-content-center h-100 text-muted">
          <div class="text-center">
            <i class="bi bi-layout-sidebar fs-3"></i>
            <div class="mt-2">从左侧打开一个工具</div>
          </div>
        </div>
      </div>
    </main>
  </div>
</template>

<style scoped>
.pane-tab {
  cursor: pointer;
  user-select: none;
}
</style>
