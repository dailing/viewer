import { defineStore } from "pinia";
import {
  activateWorkspace,
  attachWorkspaceRole,
  getWorkspaces,
  putWorkspace,
  removeWorkspaceRole,
} from "../api/client";
import type { WorkspaceData, WorkspaceSnapshot, WorkspaceSummary } from "../types/workspaces";

function uniqueIds(ids: string[]) {
  return [...new Set(ids.map((id) => id.trim()).filter(Boolean))];
}

export const useWorkspacesStore = defineStore("workspaces", {
  state: () => ({
    activeWorkspaceId: "1",
    count: 5,
    workspaces: [] as WorkspaceSummary[],
    slots: {} as Record<string, WorkspaceSnapshot>,
    activeAgentSessionRefs: [] as string[],
    loaded: false,
    switching: false,
  }),
  actions: {
    applyData(data: WorkspaceData, fallbackActiveId?: string, preserveActive = false) {
      this.activeWorkspaceId = preserveActive ? this.activeWorkspaceId : data.active_workspace_id || fallbackActiveId || "1";
      this.count = Math.min(20, Math.max(1, Math.round(Number(data.count || 5))));
      this.workspaces = data.workspaces?.length
        ? data.workspaces
        : Array.from({ length: this.count }, (_, index) => {
            const id = String(index + 1);
            return { id, name: `Traditional Workspace ${id}`, created_at: 0, updated_at: 0 };
          });
      this.slots = data.slots ?? {};
      this.loaded = true;
    },
    async load() {
      const data = await getWorkspaces();
      this.applyData(data);
    },
    snapshotFor(id: string) {
      return this.slots[id] ?? null;
    },
    restoreActiveAgentSessions(snapshot: WorkspaceSnapshot | null) {
      this.activeAgentSessionRefs = uniqueIds(snapshot?.agent_session_ids ?? []);
    },
    async rememberActiveAgentSession(ref: string, workspaceId?: string) {
      const targetWorkspaceId = workspaceId ?? this.activeWorkspaceId;
      this.activeAgentSessionRefs = uniqueIds([...this.activeAgentSessionRefs, ref]);
      const data = await attachWorkspaceRole(targetWorkspaceId, ref);
      this.applyData(data, targetWorkspaceId, true);
      if (this.activeWorkspaceId === targetWorkspaceId) this.restoreActiveAgentSessions(this.slots[targetWorkspaceId] ?? null);
    },
    async forgetActiveAgentSession(ref: string, workspaceId?: string) {
      const targetWorkspaceId = workspaceId ?? this.activeWorkspaceId;
      this.activeAgentSessionRefs = this.activeAgentSessionRefs.filter((item) => item !== ref);
      const data = await removeWorkspaceRole(targetWorkspaceId, ref);
      this.applyData(data, targetWorkspaceId, true);
      if (this.activeWorkspaceId === targetWorkspaceId) this.restoreActiveAgentSessions(this.slots[targetWorkspaceId] ?? null);
    },
    async saveSlot(id: string, snapshot: WorkspaceSnapshot, options?: { apply?: boolean; restoreActive?: boolean }) {
      const data = await putWorkspace(id, { ...snapshot, updated_at: Date.now() / 1000 });
      if (options?.apply !== false) this.applyData(data, id, true);
      if (options?.restoreActive !== false && this.activeWorkspaceId === id) this.restoreActiveAgentSessions(this.slots[id] ?? snapshot);
      return data;
    },
    async activate(id: string, options?: { apply?: boolean }) {
      const data = await activateWorkspace(id);
      if (options?.apply !== false) this.applyData(data, id);
      return data;
    },
  },
});
