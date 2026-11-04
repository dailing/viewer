<script setup lang="ts">
import { ref } from "vue";

import ChatsPanel from "./ChatsPanel.vue";
import RolesPanel from "./RolesPanel.vue";
import RoutesPanel from "./RoutesPanel.vue";

type Tab = "chats" | "roles" | "routes";
const activeTab = ref<Tab>("chats");
const tabs: Array<{ id: Tab; label: string; icon: string }> = [
  { id: "chats", label: "聊天", icon: "bi-chat-left-text" },
  { id: "roles", label: "Roles", icon: "bi-people" },
  { id: "routes", label: "路由", icon: "bi-signpost-split" },
];
</script>

<template>
  <section class="d-flex flex-column h-100 v-bg-surface">
    <div class="px-3 py-2 border-bottom">
      <div class="btn-group btn-group-sm" role="tablist" aria-label="聊天设置">
        <button
          v-for="tab in tabs"
          :key="tab.id"
          type="button"
          class="btn"
          :class="activeTab === tab.id ? 'btn-primary' : 'btn-outline-secondary'"
          role="tab"
          :aria-selected="activeTab === tab.id"
          @click="activeTab = tab.id"
        >
          <i class="bi me-1" :class="tab.icon"></i>{{ tab.label }}
        </button>
      </div>
    </div>
    <div class="flex-grow-1 min-h-0">
      <ChatsPanel v-if="activeTab === 'chats'" />
      <RolesPanel v-else-if="activeTab === 'roles'" />
      <RoutesPanel v-else />
    </div>
  </section>
</template>

<style scoped>
.min-h-0 { min-height: 0; }
</style>
