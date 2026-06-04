import { defineStore } from "pinia";
import {
  createHermesSession,
  deleteHermesQueuedMessage,
  listHermesSessions,
  queueHermesMessage,
  sendHermesMessage,
  terminateHermesSession,
  updateHermesQueuedMessage,
} from "../api/client";
import type { HermesSessionInfo } from "../types/hermes";

export const useHermesStore = defineStore("hermes", {
  state: () => ({
    sessions: [] as HermesSessionInfo[],
    unreadSessionIds: [] as string[],
    loading: false,
  }),
  actions: {
    async load() {
      this.loading = true;
      try {
        this.sessions = await listHermesSessions();
      } finally {
        this.loading = false;
      }
    },
    async create(prompt: string, cwd = "") {
      const session = await createHermesSession(prompt, cwd);
      this.upsert(session);
      return session;
    },
    async send(id: string, prompt: string) {
      const session = await sendHermesMessage(id, prompt);
      this.upsert(session);
      return session;
    },
    async queue(id: string, prompt: string) {
      const session = await queueHermesMessage(id, prompt);
      this.upsert(session);
      return session;
    },
    async updateQueued(id: string, itemId: string, prompt: string) {
      const session = await updateHermesQueuedMessage(id, itemId, prompt);
      this.upsert(session);
      return session;
    },
    async deleteQueued(id: string, itemId: string) {
      const session = await deleteHermesQueuedMessage(id, itemId);
      this.upsert(session);
      return session;
    },
    async terminate(id: string) {
      const session = await terminateHermesSession(id);
      this.upsert(session);
      return session;
    },
    upsert(session: HermesSessionInfo) {
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
