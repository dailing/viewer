<script setup lang="ts">
import { computed } from "vue";

import { busState } from "./shell/bus";
import PluginPaneHost from "./shell/PluginPaneHost.vue";
import { sidebarTools } from "./shell/registries";
import { useShellStore } from "./stores/shell";

const shell = useShellStore();

const activePane = computed(() =>
  shell.panes.find((pane) => pane.instanceId === shell.activeInstanceId),
);

const connectionBadge = computed(() => (busState.connected ? "text-bg-success" : "text-bg-danger"));
const connectionText = computed(() => (busState.connected ? "已连接" : "连接中…"));
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
      <header class="d-flex align-items-center border-bottom px-3 py-2">
        <strong>Viewer</strong>
        <span class="text-muted small ms-2">plugin workbench</span>
        <span class="badge ms-auto" :class="connectionBadge">{{ connectionText }}</span>
      </header>
      <div class="flex-grow-1 overflow-hidden position-relative">
        <PluginPaneHost
          v-if="activePane !== undefined"
          :key="activePane.instanceId"
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
