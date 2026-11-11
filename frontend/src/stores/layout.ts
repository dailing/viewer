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

/** How tree panes are arranged. Floating panes are outside every mode. */
export type LayoutMode = "cascade" | "columns" | "rows" | "free";

/** A pane lifted out of the split tree, rendered in the floating overlay layer. */
export interface FloatingPane {
  id: string;
  content: PaneContent;
  /** Position/size in px relative to the workspace area. */
  x: number;
  y: number;
  w: number;
  h: number;
  /** Stacking order; highest z is frontmost. */
  z: number;
  /** Bumped by refreshFloating: remounts the hosted component via :key. */
  epoch: number;
}

export const FLOAT_MIN_W = 240;
export const FLOAT_MIN_H = 160;

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

/** How openInstance places new content: split a fresh pane, or replace the active pane's content. */
export type OpenMode = "new" | "replace";

const OPEN_MODE_STORAGE_KEY = "viewer.layout.openMode.v1";
const LAYOUT_MODE_STORAGE_KEY = "viewer.layout.mode.v1";
export const WORKSPACE_STORAGE_KEY = "viewer.layout.workspace.v1";

export interface WorkspaceSnapshot {
  root: LayoutNode;
  activePaneId: string;
  floating: FloatingPane[];
}

function emptyWorkspace(): WorkspaceSnapshot {
  return {
    root: { type: "pane", id: "p1", content: null, epoch: 0 },
    activePaneId: "p1",
    floating: [],
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function parseContent(value: unknown, allowEmpty: boolean): PaneContent | null {
  if (value === null && allowEmpty) return null;
  if (!isRecord(value) || typeof value.paneType !== "string" || value.paneType === "" ||
      typeof value.instanceId !== "string" || value.instanceId === "") {
    throw new Error("invalid pane content");
  }
  return { paneType: value.paneType, instanceId: value.instanceId };
}

function parseLayoutNode(
  value: unknown,
  ids: Set<string>,
  paneIds: Set<string>,
  contents: Set<string>,
): LayoutNode {
  if (!isRecord(value) || typeof value.id !== "string" || value.id === "" || ids.has(value.id)) {
    throw new Error("invalid layout node");
  }
  ids.add(value.id);
  if (value.type === "pane") {
    const content = parseContent(value.content, true);
    if (content !== null) {
      const uid = contentUid(content);
      if (contents.has(uid)) throw new Error("duplicate pane content");
      contents.add(uid);
    }
    paneIds.add(value.id);
    return { type: "pane", id: value.id, content, epoch: 0 };
  }
  if (value.type !== "split" || (value.direction !== "horizontal" && value.direction !== "vertical") ||
      typeof value.ratio !== "number" || !Number.isFinite(value.ratio)) {
    throw new Error("invalid split node");
  }
  return {
    type: "split",
    id: value.id,
    direction: value.direction,
    ratio: Math.min(RATIO_MAX, Math.max(RATIO_MIN, value.ratio)),
    first: parseLayoutNode(value.first, ids, paneIds, contents),
    second: parseLayoutNode(value.second, ids, paneIds, contents),
  };
}

function parseFloatingPane(
  value: unknown,
  ids: Set<string>,
  contents: Set<string>,
): FloatingPane {
  if (!isRecord(value) || typeof value.id !== "string" || value.id === "" || ids.has(value.id)) {
    throw new Error("invalid floating pane");
  }
  const content = parseContent(value.content, false);
  if (content === null) throw new Error("empty floating pane");
  const uid = contentUid(content);
  if (contents.has(uid)) throw new Error("duplicate floating content");
  for (const field of ["x", "y", "w", "h", "z"] as const) {
    if (typeof value[field] !== "number" || !Number.isFinite(value[field])) {
      throw new Error("invalid floating geometry");
    }
  }
  ids.add(value.id);
  contents.add(uid);
  return {
    id: value.id,
    content,
    x: Math.round(value.x as number),
    y: Math.round(value.y as number),
    w: Math.max(FLOAT_MIN_W, Math.round(value.w as number)),
    h: Math.max(FLOAT_MIN_H, Math.round(value.h as number)),
    z: Math.max(1, Math.round(value.z as number)),
    epoch: 0,
  };
}

function loadWorkspace(): WorkspaceSnapshot {
  try {
    const raw = localStorage.getItem(WORKSPACE_STORAGE_KEY);
    if (raw === null) return emptyWorkspace();
    const value: unknown = JSON.parse(raw);
    if (!isRecord(value) || typeof value.activePaneId !== "string" || !Array.isArray(value.floating)) {
      throw new Error("invalid workspace snapshot");
    }
    const ids = new Set<string>();
    const paneIds = new Set<string>();
    const contents = new Set<string>();
    const root = parseLayoutNode(value.root, ids, paneIds, contents);
    if (!paneIds.has(value.activePaneId)) throw new Error("invalid active pane");
    const floating = value.floating.map((entry) => parseFloatingPane(entry, ids, contents));
    return { root, activePaneId: value.activePaneId, floating };
  } catch {
    return emptyWorkspace();
  }
}

function nextWorkspaceId(snapshot: WorkspaceSnapshot): number {
  const ids: string[] = [];
  const visit = (node: LayoutNode): void => {
    ids.push(node.id);
    if (node.type === "split") {
      visit(node.first);
      visit(node.second);
    }
  };
  visit(snapshot.root);
  ids.push(...snapshot.floating.map((pane) => pane.id));
  let next = 1;
  for (const id of ids) {
    const match = /^p(\d+)$/.exec(id);
    if (match !== null) next = Math.max(next, Number(match[1]) + 1);
  }
  return next;
}

function readOpenMode(): OpenMode {
  return localStorage.getItem(OPEN_MODE_STORAGE_KEY) === "replace" ? "replace" : "new";
}

function readLayoutMode(): LayoutMode {
  const value = localStorage.getItem(LAYOUT_MODE_STORAGE_KEY);
  return value === "columns" || value === "rows" || value === "free" ? value : "cascade";
}

export const useLayoutStore = defineStore("layout", {
  state: () => {
    const workspace = loadWorkspace();
    return {
      root: workspace.root,
      activePaneId: workspace.activePaneId,
      nextId: nextWorkspaceId(workspace),
      openMode: readOpenMode() as OpenMode,
      mode: readLayoutMode() as LayoutMode,
      floating: workspace.floating,
      nextZ: Math.max(1, ...workspace.floating.map((pane) => pane.z + 1)),
    };
  },
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
      if (this.floating.some((pane) => contentUid(pane.content) === uid)) return true;
      return this.panes.some((pane) => pane.content !== null && contentUid(pane.content) === uid);
    },
    /**
     * Focus an open instance, reuse an empty pane, or place new content:
     * "new" mode splits without replacing content; "replace" mode swaps the
     * active pane's content in place (mobile-friendly single-pane flow).
     * opts.newPane forces a fresh split off the active pane regardless of
     * openMode and empty-tile reuse (explicit "open in a new panel" entries).
     */
    openInstance(paneType: string, instanceId: string, opts?: { newPane?: boolean }): string {
      const uid = `${paneType}:${instanceId}`;
      const floated = this.floating.find((pane) => contentUid(pane.content) === uid);
      if (floated !== undefined) {
        this.raiseFloating(floated.id);
        return floated.id;
      }
      const existing = this.panes.find(
        (pane) => pane.content !== null && contentUid(pane.content) === uid,
      );
      if (existing !== undefined) {
        this.activePaneId = existing.id;
        return existing.id;
      }
      let target = this.activePane;
      if (target.content !== null) {
        if (opts?.newPane === true) {
          this.splitPane(target.id, "vertical");
          target = this.activePane;
        } else {
          const empty = this.panes.find((pane) => pane.content === null);
          if (empty !== undefined) {
            target = empty;
          } else if (this.openMode !== "replace") {
            // Algorithmic layouts have one stable placement rule: new tiles
            // enter at the leaf end. Free mode keeps the focused-pane split
            // behavior because its geometry is explicitly user-authored.
            const splitTarget = this.mode === "free" ? target : this.panes[this.panes.length - 1];
            this.splitPane(splitTarget.id, "vertical");
            target = this.activePane;
          }
        }
      }
      target.content = { paneType, instanceId };
      this.activePaneId = target.id;
      return target.id;
    },
    setOpenMode(mode: OpenMode): void {
      this.openMode = mode;
      localStorage.setItem(OPEN_MODE_STORAGE_KEY, mode);
    },
    setMode(mode: LayoutMode): void {
      this.mode = mode;
      localStorage.setItem(LAYOUT_MODE_STORAGE_KEY, mode);
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
    /** Add an unassigned tile at the leaf end; Dock opens can then reuse it. */
    addPane(): void {
      const panes = this.panes;
      const target = panes[panes.length - 1];
      if (target !== undefined) this.splitPane(target.id, "vertical");
    },
    /** Move the focused content one tile toward the root/start or leaf/end. */
    movePane(paneId: string, delta: -1 | 1): void {
      const panes = this.panes;
      const from = panes.findIndex((pane) => pane.id === paneId);
      const to = from + delta;
      if (from < 0 || to < 0 || to >= panes.length) return;
      const source = panes[from];
      const destination = panes[to];
      [source.content, destination.content] = [destination.content, source.content];
      [source.epoch, destination.epoch] = [destination.epoch, source.epoch];
      this.activePaneId = destination.id;
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
    /** Lift a tree pane's content into the floating layer; the freed pane closes. */
    floatPane(paneId: string): void {
      const node = findNode(this.root, paneId);
      if (node === null || node.type !== "pane" || node.content === null) return;
      const content = node.content;
      this.closePane(paneId);
      const offset = 24 + (this.floating.length % 5) * 32;
      this.floating.push({
        id: this.newId(),
        content,
        x: offset,
        y: offset,
        w: 520,
        h: 380,
        z: this.nextZ++,
        epoch: 0,
      });
    },
    /**
     * Return a floating pane's content to the split tree without displacing
     * existing tiles: tiling modes append a fresh tile at the leaf end
     * (filling the last tile instead when it is empty); free mode replaces
     * the active pane, since its geometry is explicitly user-authored.
     */
    dockFloating(id: string): void {
      const index = this.floating.findIndex((pane) => pane.id === id);
      if (index < 0) return;
      const [pane] = this.floating.splice(index, 1);
      const content = pane.content;
      if (this.mode === "free") {
        const target = this.activePane;
        target.content = content;
        this.activePaneId = target.id;
        return;
      }
      const panes = this.panes;
      const last = panes[panes.length - 1];
      if (last !== undefined && last.content === null) {
        last.content = content;
        this.activePaneId = last.id;
        return;
      }
      if (last !== undefined) {
        this.splitPane(last.id, "vertical");
        this.activePane.content = content; // splitPane focuses the fresh tile
      }
    },
    closeFloating(id: string): void {
      const index = this.floating.findIndex((pane) => pane.id === id);
      if (index >= 0) this.floating.splice(index, 1);
    },
    raiseFloating(id: string): void {
      const pane = this.floating.find((entry) => entry.id === id);
      if (pane !== undefined && pane.z !== this.nextZ - 1) pane.z = this.nextZ++;
    },
    moveFloating(id: string, x: number, y: number): void {
      const pane = this.floating.find((entry) => entry.id === id);
      if (pane === undefined) return;
      pane.x = Math.round(x);
      pane.y = Math.round(y);
    },
    resizeFloating(id: string, w: number, h: number): void {
      const pane = this.floating.find((entry) => entry.id === id);
      if (pane === undefined) return;
      pane.w = Math.max(FLOAT_MIN_W, Math.round(w));
      pane.h = Math.max(FLOAT_MIN_H, Math.round(h));
    },
    refreshFloating(id: string): void {
      const pane = this.floating.find((entry) => entry.id === id);
      if (pane !== undefined) pane.epoch += 1;
    },
  },
});

/**
 * Restore happens in the store initializer, before plugin activation. Persist
 * subsequent mutations with a short debounce so floating-pane drags do not
 * synchronously serialize the workspace for every pointer event. `pagehide`
 * flushes the latest state before a refresh or tab close.
 */
export function startLayoutPersistence(): () => void {
  const layout = useLayoutStore();
  let timer: ReturnType<typeof setTimeout> | null = null;

  const save = (): void => {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
    const snapshot: WorkspaceSnapshot = {
      root: layout.root,
      activePaneId: layout.activePaneId,
      floating: layout.floating,
    };
    try {
      localStorage.setItem(WORKSPACE_STORAGE_KEY, JSON.stringify(snapshot));
    } catch {
      // Storage denial/quota must not break workspace interactions.
    }
  };
  const schedule = (): void => {
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(save, 150);
  };
  const unsubscribe = layout.$subscribe(schedule, { detached: true, flush: "post" });
  if (typeof window !== "undefined") window.addEventListener("pagehide", save);
  save();

  return () => {
    unsubscribe();
    if (typeof window !== "undefined") window.removeEventListener("pagehide", save);
    if (timer !== null) clearTimeout(timer);
  };
}
