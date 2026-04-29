import { defineStore } from "pinia";
import { createCodexSession, deleteCodexSession, listCodexSessions, sendCodexMessage } from "../api/client";
import type { CodexSessionInfo } from "../types/codex";

export const useCodexStore = defineStore("codex", {
  state: () => ({
    sessions: [] as CodexSessionInfo[],
    loading: false,
  }),
  actions: {
    async load() {
      this.loading = true;
      try {
        this.sessions = await listCodexSessions();
      } finally {
        this.loading = false;
      }
    },
    async create(prompt: string, cwd = "") {
      const session = await createCodexSession(prompt, cwd);
      this.upsert(session);
      return session;
    },
    async send(id: string, prompt: string) {
      const session = await sendCodexMessage(id, prompt);
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
