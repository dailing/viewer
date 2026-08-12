/**
 * Shell store: open panes + active pane. The layout is deliberately minimal
 * (one visible pane, sidebar switching) until the layout plugin lands in
 * Phase 5 (framework section 13).
 */

import { defineStore } from "pinia";

export interface Pane {
  paneType: string;
  instanceId: string;
}

export const useShellStore = defineStore("shell", {
  state: () => ({
    panes: [] as Pane[],
    activeInstanceId: null as string | null,
  }),
  actions: {
    openPane(paneType: string): void {
      // One pane per type for now; multi-instance arrives with the layout plugin.
      const existing = this.panes.find((pane) => pane.paneType === paneType);
      if (existing !== undefined) {
        this.activeInstanceId = existing.instanceId;
        return;
      }
      const pane: Pane = { paneType, instanceId: "main" };
      this.panes.push(pane);
      this.activeInstanceId = pane.instanceId;
    },
    closePane(instanceId: string): void {
      const index = this.panes.findIndex((pane) => pane.instanceId === instanceId);
      if (index < 0) return;
      this.panes.splice(index, 1);
      if (this.activeInstanceId === instanceId) {
        this.activeInstanceId = this.panes[index - 1]?.instanceId ?? this.panes[0]?.instanceId ?? null;
      }
    },
    setActive(instanceId: string): void {
      this.activeInstanceId = instanceId;
    },
  },
});
