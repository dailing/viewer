import { defineStore } from "pinia";
import {
  activateWorkspace,
  addWorkspaceAgentSession,
  addWorkspacePinnedAgentSession,
  getWorkspaces,
  putWorkspace,
  removeWorkspaceAgentSession,
  removeWorkspacePinnedAgentSession,
} from "../api/client";
import type { WorkspaceData, WorkspaceSnapshot } from "../types/workspaces";

function uniqueIds(ids: string[]) {
  return [...new Set(ids.map((id) => id.trim()).filter(Boolean))];
}

export const useWorkspacesStore = defineStore("workspaces", {
  state: () => ({
    activeWorkspaceId: "1",
    count: 5,
    slots: {} as Record<string, WorkspaceSnapshot>,
    activeAgentSessionRefs: [] as string[],
    activePinnedAgentSessionRefs: [] as string[],
    loaded: false,
    switching: false,
  }),
  actions: {
    applyData(data: WorkspaceData, fallbackActiveId?: string, preserveActive = false) {
      this.activeWorkspaceId = preserveActive ? this.activeWorkspaceId : data.active_workspace_id || fallbackActiveId || "1";
      this.count = Math.min(20, Math.max(1, Math.round(Number(data.count || 5))));
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
      this.activePinnedAgentSessionRefs = uniqueIds(snapshot?.pinned_agent_session_ids ?? []);
    },
    async rememberActiveAgentSession(ref: string, workspaceId?: string) {
      const targetWorkspaceId = workspaceId ?? this.activeWorkspaceId;
      this.activeAgentSessionRefs = uniqueIds([...this.activeAgentSessionRefs, ref]);
      const data = await addWorkspaceAgentSession(targetWorkspaceId, ref);
      this.applyData(data, targetWorkspaceId, true);
      if (this.activeWorkspaceId === targetWorkspaceId) this.restoreActiveAgentSessions(this.slots[targetWorkspaceId] ?? null);
    },
    async forgetActiveAgentSession(ref: string, workspaceId?: string) {
      const targetWorkspaceId = workspaceId ?? this.activeWorkspaceId;
      this.activeAgentSessionRefs = this.activeAgentSessionRefs.filter((item) => item !== ref);
      this.activePinnedAgentSessionRefs = this.activePinnedAgentSessionRefs.filter((item) => item !== ref);
      const data = await removeWorkspaceAgentSession(targetWorkspaceId, ref);
      this.applyData(data, targetWorkspaceId, true);
      if (this.activeWorkspaceId === targetWorkspaceId) this.restoreActiveAgentSessions(this.slots[targetWorkspaceId] ?? null);
    },
    async pinActiveAgentSession(ref: string, workspaceId?: string) {
      const targetWorkspaceId = workspaceId ?? this.activeWorkspaceId;
      this.activePinnedAgentSessionRefs = uniqueIds([...this.activePinnedAgentSessionRefs, ref]);
      const data = await addWorkspacePinnedAgentSession(targetWorkspaceId, ref);
      this.applyData(data, targetWorkspaceId, true);
      if (this.activeWorkspaceId === targetWorkspaceId) this.restoreActiveAgentSessions(this.slots[targetWorkspaceId] ?? null);
    },
    async unpinActiveAgentSession(ref: string, workspaceId?: string) {
      const targetWorkspaceId = workspaceId ?? this.activeWorkspaceId;
      this.activePinnedAgentSessionRefs = this.activePinnedAgentSessionRefs.filter((item) => item !== ref);
      const data = await removeWorkspacePinnedAgentSession(targetWorkspaceId, ref);
      this.applyData(data, targetWorkspaceId, true);
      if (this.activeWorkspaceId === targetWorkspaceId) this.restoreActiveAgentSessions(this.slots[targetWorkspaceId] ?? null);
    },
    async rememberActiveCodexSession(id: string, workspaceId?: string) {
      await this.rememberActiveAgentSession(`codex:${id}`, workspaceId);
    },
    async forgetActiveCodexSession(id: string, workspaceId?: string) {
      await this.forgetActiveAgentSession(`codex:${id}`, workspaceId);
    },
    async rememberActiveHermesSession(id: string, workspaceId?: string) {
      await this.rememberActiveAgentSession(`hermes:${id}`, workspaceId);
    },
    async forgetActiveHermesSession(id: string, workspaceId?: string) {
      await this.forgetActiveAgentSession(`hermes:${id}`, workspaceId);
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
