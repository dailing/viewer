import { defineStore } from "pinia";
import { activateWorkspace, getWorkspaces, putWorkspace } from "../api/client";
import type { WorkspaceSnapshot } from "../types/workspaces";

export const useWorkspacesStore = defineStore("workspaces", {
  state: () => ({
    activeWorkspaceId: "1",
    slots: {} as Record<string, WorkspaceSnapshot>,
    loaded: false,
    switching: false,
  }),
  actions: {
    async load() {
      const data = await getWorkspaces();
      this.activeWorkspaceId = data.active_workspace_id || "1";
      this.slots = data.slots ?? {};
      this.loaded = true;
    },
    snapshotFor(id: string) {
      return this.slots[id] ?? null;
    },
    async saveSlot(id: string, snapshot: WorkspaceSnapshot) {
      const data = await putWorkspace(id, { ...snapshot, updated_at: Date.now() / 1000 });
      this.activeWorkspaceId = data.active_workspace_id || id;
      this.slots = data.slots ?? {};
      this.loaded = true;
    },
    async activate(id: string) {
      const data = await activateWorkspace(id);
      this.activeWorkspaceId = data.active_workspace_id || id;
      this.slots = data.slots ?? {};
      this.loaded = true;
    },
  },
});
