/**
 * Shell store: open panes + active pane. The layout is deliberately minimal
 * (one visible pane, sidebar switching + a small tab strip) until the layout
 * plugin lands in Phase 5 (framework section 13).
 */

import { defineStore } from "pinia";

export interface Pane {
  /** Composite identity `${paneType}:${instanceId}` — unique across types. */
  uid: string;
  paneType: string;
  instanceId: string;
}

export const useShellStore = defineStore("shell", {
  state: () => ({
    panes: [] as Pane[],
    activeUid: null as string | null,
  }),
  actions: {
    openPane(paneType: string, instanceId = "main"): void {
      const uid = `${paneType}:${instanceId}`;
      const existing = this.panes.find((pane) => pane.uid === uid);
      if (existing !== undefined) {
        this.activeUid = existing.uid;
        return;
      }
      this.panes.push({ uid, paneType, instanceId });
      this.activeUid = uid;
    },
    closePane(uid: string): void {
      const index = this.panes.findIndex((pane) => pane.uid === uid);
      if (index < 0) return;
      this.panes.splice(index, 1);
      if (this.activeUid === uid) {
        this.activeUid = this.panes[index - 1]?.uid ?? this.panes[0]?.uid ?? null;
      }
    },
    setActive(uid: string): void {
      this.activeUid = uid;
    },
  },
});
