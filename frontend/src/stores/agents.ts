import { defineStore } from "pinia";
import { listAgentProviders } from "../api/client";
import type { AgentProvider, AgentProviderInfo } from "../types/agents";

const DEFAULT_PROVIDERS: AgentProviderInfo[] = [
  { id: "codex", name: "Codex", icon: "bi-stars" },
  { id: "hermes", name: "Hermes", icon: "bi-lightning-charge" },
];

export const useAgentsStore = defineStore("agents", {
  state: () => ({
    providers: DEFAULT_PROVIDERS as AgentProviderInfo[],
    providersLoaded: false,
  }),
  getters: {
    providerById(state) {
      return (id: AgentProvider) => state.providers.find((provider) => provider.id === id) ?? { id, name: id, icon: "bi-cpu" };
    },
  },
  actions: {
    async loadProviders() {
      try {
        this.providers = await listAgentProviders();
        this.providersLoaded = true;
      } catch {
        this.providers = DEFAULT_PROVIDERS;
        this.providersLoaded = true;
      }
    },
  },
});
