import { defineStore } from "pinia";
import {
  createAgentSession,
  getAgentSession,
  listAgentProviders,
  listAgentSessions,
  queueAgentMessage,
  resolveAgentApproval,
  sendAgentMessage,
  terminateAgentSession,
} from "../api/client";
import type { AgentProvider, AgentProviderInfo, AgentSessionInfo, AgentSessionSnapshot } from "../types/agents";
import type { CodexSessionInfo, CodexSessionSnapshot } from "../types/codex";
import type { HermesSessionInfo, HermesSessionSnapshot } from "../types/hermes";
import { parseAgentRef, toAgentSessionInfo, toAgentSessionSnapshot } from "../utils/agents";

const DEFAULT_PROVIDERS: AgentProviderInfo[] = [
  { id: "codex", name: "Codex", icon: "bi-stars" },
  { id: "hermes", name: "Hermes", icon: "bi-lightning-charge" },
];

export function sortAgentSessions(sessions: AgentSessionInfo[]) {
  return [...sessions].sort((left, right) => {
    const updatedDiff = right.updated_at - left.updated_at;
    if (updatedDiff !== 0) return updatedDiff;

    const createdDiff = right.created_at - left.created_at;
    if (createdDiff !== 0) return createdDiff;

    return left.title.localeCompare(right.title);
  });
}

export const useAgentsStore = defineStore("agents", {
  state: () => ({
    providers: DEFAULT_PROVIDERS as AgentProviderInfo[],
    providersLoaded: false,
    sessions: [] as AgentSessionInfo[],
    unreadSessionRefs: [] as string[],
    loading: false,
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
    async load() {
      if (this.loading) return;
      this.loading = true;
      try {
        if (!this.providersLoaded) await this.loadProviders();
        const groups = await Promise.all(
          this.providers.map(async (provider) => {
            const sessions = await listAgentSessions(provider.id);
            return sessions.map((session) => toAgentSessionInfo(session as CodexSessionInfo | HermesSessionInfo, provider.id));
          }),
        );
        this.sessions = sortAgentSessions(groups.flat());
      } finally {
        this.loading = false;
      }
    },
    async snapshot(ref: string, detail: "focus" | "full" = "focus"): Promise<AgentSessionSnapshot> {
      const parsed = parseAgentRef(ref);
      if (!parsed) throw new Error("Invalid agent session reference");
      const session = await getAgentSession(parsed.provider, parsed.id, detail);
      return toAgentSessionSnapshot(session as CodexSessionSnapshot | HermesSessionSnapshot, parsed.provider);
    },
    async create(provider: AgentProvider, prompt: string, cwd = "", model?: string) {
      const session = await createAgentSession(provider, prompt, cwd, model);
      const next = toAgentSessionInfo(session as CodexSessionInfo | HermesSessionInfo, provider);
      this.upsert(next);
      return next;
    },
    async send(ref: string, prompt: string, model?: string) {
      const parsed = parseAgentRef(ref);
      if (!parsed) throw new Error("Invalid agent session reference");
      const session = await sendAgentMessage(parsed.provider, parsed.id, prompt, model);
      const next = toAgentSessionInfo(session as CodexSessionInfo | HermesSessionInfo, parsed.provider);
      this.upsert(next);
      return next;
    },
    async queue(ref: string, prompt: string, model?: string) {
      const parsed = parseAgentRef(ref);
      if (!parsed) throw new Error("Invalid agent session reference");
      const session = await queueAgentMessage(parsed.provider, parsed.id, prompt, model);
      const next = toAgentSessionInfo(session as CodexSessionInfo | HermesSessionInfo, parsed.provider);
      this.upsert(next);
      return next;
    },
    async terminate(ref: string) {
      const parsed = parseAgentRef(ref);
      if (!parsed) throw new Error("Invalid agent session reference");
      const session = await terminateAgentSession(parsed.provider, parsed.id);
      const next = toAgentSessionInfo(session as CodexSessionInfo | HermesSessionInfo, parsed.provider);
      this.upsert(next);
      return next;
    },
    async resolveApproval(ref: string, approvalId: string, choice: string, all = false) {
      const parsed = parseAgentRef(ref);
      if (!parsed) throw new Error("Invalid agent session reference");
      const session = await resolveAgentApproval(parsed.provider, parsed.id, approvalId, choice, all);
      const next = toAgentSessionInfo(session as CodexSessionInfo | HermesSessionInfo, parsed.provider);
      this.upsert(next);
      return next;
    },
    upsert(session: AgentSessionInfo) {
      const index = this.sessions.findIndex((item) => item.ref === session.ref);
      if (index === -1) {
        this.sessions = sortAgentSessions([session, ...this.sessions]);
        return;
      }
      this.sessions = sortAgentSessions(this.sessions.map((item) => (item.ref === session.ref ? session : item)));
    },
    markUnread(ref: string) {
      if (this.unreadSessionRefs.includes(ref)) return;
      this.unreadSessionRefs = [...this.unreadSessionRefs, ref];
    },
    markRead(ref: string) {
      if (!this.unreadSessionRefs.includes(ref)) return;
      this.unreadSessionRefs = this.unreadSessionRefs.filter((item) => item !== ref);
    },
  },
});
