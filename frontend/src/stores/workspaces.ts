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
    rememberActiveCodexSession(id: string) {
      this.activeCodexSessionIds = uniqueIds([...this.activeCodexSessionIds, id]);
    },
    forgetActiveCodexSession(id: string) {
      this.activeCodexSessionIds = this.activeCodexSessionIds.filter((item) => item !== id);
    },
    async saveSlot(id: string, snapshot: WorkspaceSnapshot) {
      const data = await putWorkspace(id, { ...snapshot, updated_at: Date.now() / 1000 });
      this.activeWorkspaceId = data.active_workspace_id || id;
      this.count = Math.min(20, Math.max(1, Math.round(Number(data.count || this.count))));
      this.slots = data.slots ?? {};
      if (this.activeWorkspaceId === id) this.restoreActiveCodexSessions(this.slots[id] ?? snapshot);
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
