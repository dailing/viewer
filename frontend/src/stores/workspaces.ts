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
      this.restoreActiveCodexSessions(snapshot);
      this.restoreActiveHermesSessions(snapshot);
    },
    rememberActiveCodexSession(id: string) {
      this.activeCodexSessionIds = uniqueIds([...this.activeCodexSessionIds, id]);
    },
    forgetActiveCodexSession(id: string) {
      this.activeCodexSessionIds = this.activeCodexSessionIds.filter((item) => item !== id);
    },
    rememberActiveHermesSession(id: string) {
      this.activeHermesSessionIds = uniqueIds([...this.activeHermesSessionIds, id]);
    },
    forgetActiveHermesSession(id: string) {
      this.activeHermesSessionIds = this.activeHermesSessionIds.filter((item) => item !== id);
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
