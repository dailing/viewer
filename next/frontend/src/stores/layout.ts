/**
 * Layout store: the workspace is a binary tree of panes (framework section 13,
 * shell-level minimal version). Each pane is a container that can hold one
 * instance (`paneType:instanceId`); splits nest arbitrarily with draggable
 * ratios. Ported from the original viewer's layout model.
 */

import { defineStore } from "pinia";

/** "horizontal" = divider runs horizontally → children stack vertically (向下分隔). */
export type SplitDirection = "horizontal" | "vertical";

export interface PaneContent {
  paneType: string;
  instanceId: string;
}

export interface PaneNode {
  type: "pane";
  id: string;
  content: PaneContent | null;
  /** Bumped by refreshPane: remounts the hosted component via :key. */
  epoch: number;
}

export interface SplitNode {
  type: "split";
  id: string;
  direction: SplitDirection;
  ratio: number;
  first: LayoutNode;
  second: LayoutNode;
}

export type LayoutNode = PaneNode | SplitNode;

export function contentUid(content: PaneContent): string {
  return `${content.paneType}:${content.instanceId}`;
}

function findNode(node: LayoutNode, id: string): LayoutNode | null {
  if (node.id === id) return node;
  if (node.type === "split") {
    return findNode(node.first, id) ?? findNode(node.second, id);
  }
  return null;
}

/** Parent split + which slot holds `childId`; null when child is the root. */
function findSlot(
  node: LayoutNode,
  childId: string,
): { parent: SplitNode; key: "first" | "second" } | null {
  if (node.type !== "split") return null;
  if (node.first.id === childId) return { parent: node, key: "first" };
  if (node.second.id === childId) return { parent: node, key: "second" };
  return findSlot(node.first, childId) ?? findSlot(node.second, childId);
}

function firstPane(node: LayoutNode): PaneNode {
  return node.type === "pane" ? node : firstPane(node.first);
}

function collectPanes(node: LayoutNode, into: PaneNode[]): PaneNode[] {
  if (node.type === "pane") into.push(node);
  else {
    collectPanes(node.first, into);
    collectPanes(node.second, into);
  }
  return into;
}

const RATIO_MIN = 0.15;
const RATIO_MAX = 0.85;

export const useLayoutStore = defineStore("layout", {
  state: () => ({
    root: { type: "pane", id: "p1", content: null, epoch: 0 } as LayoutNode,
    activePaneId: "p1",
    nextId: 2,
  }),
  getters: {
    panes(state): PaneNode[] {
      return collectPanes(state.root, []);
    },
    activePane(state): PaneNode {
      const found = findNode(state.root, state.activePaneId);
      return found !== null && found.type === "pane" ? found : firstPane(state.root);
    },
  },
  actions: {
    newId(): string {
      const id = `p${this.nextId}`;
      this.nextId += 1;
      return id;
    },
    isUidOpen(uid: string): boolean {
      return this.panes.some((pane) => pane.content !== null && contentUid(pane.content) === uid);
    },
    /** Focus the pane already showing this instance, else open it in the active pane. */
    openInstance(paneType: string, instanceId: string): void {
      const uid = `${paneType}:${instanceId}`;
      const existing = this.panes.find(
        (pane) => pane.content !== null && contentUid(pane.content) === uid,
      );
      if (existing !== undefined) {
        this.activePaneId = existing.id;
        return;
      }
      const target = this.activePane;
      target.content = { paneType, instanceId };
      this.activePaneId = target.id;
    },
    setActivePane(paneId: string): void {
      this.activePaneId = paneId;
    },
    /** Split a pane; the new empty pane takes the second slot and focus. */
    splitPane(paneId: string, direction: SplitDirection): void {
      const node = findNode(this.root, paneId);
      if (node === null || node.type !== "pane") return;
      const fresh: PaneNode = { type: "pane", id: this.newId(), content: null, epoch: 0 };
      const split: SplitNode = {
        type: "split",
        id: this.newId(),
        direction,
        ratio: 0.5,
        first: node,
        second: fresh,
      };
      const slot = findSlot(this.root, paneId);
      if (slot === null) this.root = split;
      else slot.parent[slot.key] = split;
      this.activePaneId = fresh.id;
    },
    /** Close a pane; the sibling subtree collapses into the freed slot. */
    closePane(paneId: string): void {
      const node = findNode(this.root, paneId);
      if (node === null || node.type !== "pane") return;
      const slot = findSlot(this.root, paneId);
      if (slot === null) {
        // Root pane: just clear its content.
        node.content = null;
        return;
      }
      const sibling = slot.key === "first" ? slot.parent.second : slot.parent.first;
      const grandSlot = findSlot(this.root, slot.parent.id);
      if (grandSlot === null) this.root = sibling;
      else grandSlot.parent[grandSlot.key] = sibling;
      if (this.activePaneId === paneId) {
        this.activePaneId = firstPane(sibling).id;
      }
    },
    refreshPane(paneId: string): void {
      const node = findNode(this.root, paneId);
      if (node !== null && node.type === "pane") node.epoch += 1;
    },
    setRatio(splitId: string, ratio: number): void {
      const node = findNode(this.root, splitId);
      if (node !== null && node.type === "split") {
        node.ratio = Math.min(RATIO_MAX, Math.max(RATIO_MIN, ratio));
      }
    },
  },
});
