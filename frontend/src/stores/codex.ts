import { defineStore } from "pinia";
import { createCodexSession, deleteCodexSession, getCodexModels, getCodexStatus, listCodexSessions, sendCodexMessage, terminateCodexSession } from "../api/client";
import type { CodexCliStatus, CodexModelOptions, CodexSessionInfo } from "../types/codex";

export const useCodexStore = defineStore("codex", {
  state: () => ({
    sessions: [] as CodexSessionInfo[],
    status: { available: false } as CodexCliStatus,
    models: { selected_model: "gpt-5.5", available_models: ["gpt-5.5"], source: "config" } as CodexModelOptions,
    modelManuallySelected: false,
    loading: false,
  }),
  actions: {
    async load() {
      this.loading = true;
      try {
        const [sessions, status, models] = await Promise.all([listCodexSessions(), getCodexStatus(), getCodexModels()]);
        this.sessions = sessions;
        this.status = status;
        if (!this.modelManuallySelected) {
          this.models = models;
        } else {
          const selected = this.models.selected_model;
          this.models = {
            ...models,
            selected_model: selected,
            available_models: models.available_models.includes(selected) ? models.available_models : [selected, ...models.available_models],
          };
        }
      } finally {
        this.loading = false;
      }
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
      const session = await createCodexSession(prompt, cwd, this.models.selected_model);
      this.upsert(session);
      return session;
    },
    async send(id: string, prompt: string) {
      const session = await sendCodexMessage(id, prompt, this.models.selected_model);
      this.upsert(session);
      return session;
    },
    async terminate(id: string) {
      const session = await terminateCodexSession(id);
      this.upsert(session);
      return session;
    },
    upsert(session: CodexSessionInfo) {
      const index = this.sessions.findIndex((item) => item.id === session.id);
      if (index === -1) {
        this.sessions = [session, ...this.sessions];
        return;
      }
      this.sessions = this.sessions.map((item) => (item.id === session.id ? session : item));
    },
    async remove(id: string) {
      await deleteCodexSession(id);
      this.sessions = this.sessions.filter((item) => item.id !== id);
    },
  },
});
