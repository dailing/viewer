import { defineStore } from "pinia";
import { activateWorkspace, getWorkspaces, putWorkspace } from "../api/client";
import type { WorkspaceSnapshot } from "../types/workspaces";

function uniqueIds(ids: string[]) {
  return [...new Set(ids.map((id) => id.trim()).filter(Boolean))];
}

export const useWorkspacesStore = defineStore("workspaces", {
  state: () => ({
    activeWorkspaceId: "1",
    count: 5,
    slots: {} as Record<string, WorkspaceSnapshot>,
    activeAgentSessionRefs: [] as string[],
    activeCodexSessionIds: [] as string[],
    activeHermesSessionIds: [] as string[],
    loaded: false,
    switching: false,
  }),
  actions: {
    async load() {
      const data = await getWorkspaces();
      this.activeWorkspaceId = data.active_workspace_id || "1";
      this.count = Math.min(20, Math.max(1, Math.round(Number(data.count || 5))));
      this.slots = data.slots ?? {};
      this.loaded = true;
    },
    snapshotFor(id: string) {
      return this.slots[id] ?? null;
    },
    restoreActiveCodexSessions(snapshot: WorkspaceSnapshot | null) {
      this.activeCodexSessionIds = uniqueIds(snapshot?.codex_session_ids ?? []);
    },
    restoreActiveHermesSessions(snapshot: WorkspaceSnapshot | null) {
      this.activeHermesSessionIds = uniqueIds(snapshot?.hermes_session_ids ?? []);
    },
    restoreActiveAgentSessions(snapshot: WorkspaceSnapshot | null) {
      this.activeAgentSessionRefs = uniqueIds([
        ...(snapshot?.agent_session_ids ?? []),
        ...(snapshot?.codex_session_ids ?? []).map((id) => `codex:${id}`),
        ...(snapshot?.hermes_session_ids ?? []).map((id) => `hermes:${id}`),
      ]);
      this.restoreActiveCodexSessions(snapshot);
      this.restoreActiveHermesSessions(snapshot);
    },
    rememberActiveAgentSession(ref: string) {
      this.activeAgentSessionRefs = uniqueIds([...this.activeAgentSessionRefs, ref]);
      if (ref.startsWith("codex:")) this.rememberActiveCodexSession(ref.slice("codex:".length));
      if (ref.startsWith("hermes:")) this.rememberActiveHermesSession(ref.slice("hermes:".length));
    },
    forgetActiveAgentSession(ref: string) {
      this.activeAgentSessionRefs = this.activeAgentSessionRefs.filter((item) => item !== ref);
      if (ref.startsWith("codex:")) this.forgetActiveCodexSession(ref.slice("codex:".length));
      if (ref.startsWith("hermes:")) this.forgetActiveHermesSession(ref.slice("hermes:".length));
    },
    rememberActiveCodexSession(id: string) {
      this.activeCodexSessionIds = uniqueIds([...this.activeCodexSessionIds, id]);
      this.activeAgentSessionRefs = uniqueIds([...this.activeAgentSessionRefs, `codex:${id}`]);
    },
    forgetActiveCodexSession(id: string) {
      this.activeCodexSessionIds = this.activeCodexSessionIds.filter((item) => item !== id);
      this.activeAgentSessionRefs = this.activeAgentSessionRefs.filter((item) => item !== `codex:${id}`);
    },
    rememberActiveHermesSession(id: string) {
      this.activeHermesSessionIds = uniqueIds([...this.activeHermesSessionIds, id]);
      this.activeAgentSessionRefs = uniqueIds([...this.activeAgentSessionRefs, `hermes:${id}`]);
    },
    forgetActiveHermesSession(id: string) {
      this.activeHermesSessionIds = this.activeHermesSessionIds.filter((item) => item !== id);
      this.activeAgentSessionRefs = this.activeAgentSessionRefs.filter((item) => item !== `hermes:${id}`);
    },
    async saveSlot(id: string, snapshot: WorkspaceSnapshot, options?: { restoreActive?: boolean }) {
      const data = await putWorkspace(id, { ...snapshot, updated_at: Date.now() / 1000 });
      this.activeWorkspaceId = data.active_workspace_id || id;
      this.count = Math.min(20, Math.max(1, Math.round(Number(data.count || this.count))));
      this.slots = data.slots ?? {};
      if (options?.restoreActive !== false && this.activeWorkspaceId === id) this.restoreActiveAgentSessions(this.slots[id] ?? snapshot);
      this.loaded = true;
    },
    async activate(id: string) {
      const data = await activateWorkspace(id);
      this.activeWorkspaceId = data.active_workspace_id || id;
      this.count = Math.min(20, Math.max(1, Math.round(Number(data.count || this.count))));
      this.slots = data.slots ?? {};
      this.loaded = true;
    },
  },
});
