<script setup lang="ts">
import { ref } from "vue";

import ChatsPanel from "./ChatsPanel.vue";
import LLMPanel from "./LLMPanel.vue";
import RolesPanel from "./RolesPanel.vue";
import RoutesPanel from "./RoutesPanel.vue";

type Tab = "chats" | "roles" | "routes" | "llm";
const activeTab = ref<Tab>("chats");
const tabs: Array<{ id: Tab; label: string; icon: string }> = [
  { id: "chats", label: "聊天", icon: "bi-chat-left-text" },
  { id: "roles", label: "Roles", icon: "bi-people" },
  { id: "routes", label: "路由", icon: "bi-signpost-split" },
  { id: "llm", label: "模型", icon: "bi-cpu" },
];
</script>

<template>
  <section class="d-flex flex-column h-100">
    <nav class="nav nav-tabs px-2 pt-2" aria-label="聊天管理">
      <button v-for="tab in tabs" :key="tab.id" type="button" class="nav-link" :class="{ active: activeTab === tab.id }" @click="activeTab = tab.id">
        <i class="bi me-1" :class="tab.icon"></i>{{ tab.label }}
      </button>
    </nav>
    <div class="flex-grow-1 min-h-0">
      <ChatsPanel v-if="activeTab === 'chats'" />
      <RolesPanel v-else-if="activeTab === 'roles'" />
      <RoutesPanel v-else-if="activeTab === 'routes'" />
      <LLMPanel v-else />
    </div>
  </section>
</template>

<style scoped>
.min-h-0 { min-height: 0; }
</style>
