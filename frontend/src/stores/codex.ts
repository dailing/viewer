import { defineStore } from "pinia";
import {
  createAgentSession,
  getCodexModels,
  getCodexStatus,
} from "../api/client";
import type { AgentSessionInfo } from "../types/agents";
import type { CodexCliStatus, CodexModelOptions, CodexSessionInfo } from "../types/codex";
import { toAgentSessionInfo } from "../utils/agents";

export const useCodexStore = defineStore("codex", {
  state: () => ({
    sessions: [] as AgentSessionInfo[],
    status: { available: false } as CodexCliStatus,
    models: { selected_model: "gpt-5.5", available_models: ["gpt-5.5"], source: "config" } as CodexModelOptions,
    modelManuallySelected: false,
    unreadSessionIds: [] as string[],
    loading: false,
  }),
  actions: {
    async load() {
      if (this.loading) return;
      this.loading = true;
      try {
        const [status, models] = await Promise.all([getCodexStatus(), getCodexModels()]);
        this.applyOptions(status, models);
      } finally {
        this.loading = false;
      }
    },
    async loadOptions() {
      if (this.loading) return;
      this.loading = true;
      try {
        const [status, models] = await Promise.all([getCodexStatus(), getCodexModels()]);
        this.applyOptions(status, models);
      } finally {
        this.loading = false;
      }
    },
    applyOptions(status: CodexCliStatus, models: CodexModelOptions) {
      this.status = status;
      if (!this.modelManuallySelected) {
        this.models = models;
        return;
      }
      const selected = this.models.selected_model;
      this.models = {
        ...models,
        selected_model: selected,
        available_models: models.available_models.includes(selected) ? models.available_models : [selected, ...models.available_models],
      };
    },
    setSelectedModel(model: string) {
      if (!model.trim()) return;
      this.modelManuallySelected = true;
      this.models.selected_model = model.trim();
      if (!this.models.available_models.includes(this.models.selected_model)) {
        this.models.available_models = [this.models.selected_model, ...this.models.available_models];
      }
    },
    async create(prompt: string, cwd = "") {
      const session = await createAgentSession("codex", prompt, cwd, this.models.selected_model);
      const next = toAgentSessionInfo(session as CodexSessionInfo, "codex");
      this.upsert(next);
      return next;
    },
    upsert(session: AgentSessionInfo) {
      const index = this.sessions.findIndex((item) => item.id === session.id);
      if (index === -1) {
        this.sessions = [session, ...this.sessions];
        return;
      }
      this.sessions = this.sessions.map((item) => (item.id === session.id ? session : item));
    },
    markUnread(id: string) {
      if (this.unreadSessionIds.includes(id)) return;
      this.unreadSessionIds = [...this.unreadSessionIds, id];
    },
    markRead(id: string) {
      if (!this.unreadSessionIds.includes(id)) return;
      this.unreadSessionIds = this.unreadSessionIds.filter((item) => item !== id);
    },
  },
});
