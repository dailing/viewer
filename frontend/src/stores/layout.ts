import { defineStore } from "pinia";
import type { LayoutNode, SplitDirection } from "../types/layout";

const STORAGE_KEY = "viewer.layout.v1";

function id(prefix = "node"): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

function defaultLayout(): LayoutNode {
  return { type: "pane", id: id("pane") };
}

function findPane(node: LayoutNode, paneId: string): LayoutNode | null {
  if (node.type === "pane") return node.id === paneId ? node : null;
  return findPane(node.first, paneId) ?? findPane(node.second, paneId);
}

function mapNode(node: LayoutNode, paneId: string, update: (pane: Extract<LayoutNode, { type: "pane" }>) => LayoutNode): LayoutNode {
  if (node.type === "pane") return node.id === paneId ? update(node) : node;
  return { ...node, first: mapNode(node.first, paneId, update), second: mapNode(node.second, paneId, update) };
}

function firstPaneId(node: LayoutNode): string {
  return node.type === "pane" ? node.id : firstPaneId(node.first);
}

function hasPane(node: LayoutNode, paneId: string): boolean {
  return Boolean(findPane(node, paneId));
}

function cloneLayout(node: LayoutNode): LayoutNode {
  return JSON.parse(JSON.stringify(node)) as LayoutNode;
}

function mapAllPanes(node: LayoutNode, update: (pane: Extract<LayoutNode, { type: "pane" }>) => LayoutNode): LayoutNode {
  if (node.type === "pane") return update(node);
  return { ...node, first: mapAllPanes(node.first, update), second: mapAllPanes(node.second, update) };
}

function removePane(node: LayoutNode, paneId: string): { node: LayoutNode; removed: boolean; promotedPaneId?: string } {
  if (node.type === "pane") {
    return { node, removed: false };
  }

  if (node.first.type === "pane" && node.first.id === paneId) {
    return { node: node.second, removed: true, promotedPaneId: firstPaneId(node.second) };
  }

  if (node.second.type === "pane" && node.second.id === paneId) {
    return { node: node.first, removed: true, promotedPaneId: firstPaneId(node.first) };
  }

  const firstResult = removePane(node.first, paneId);
  if (firstResult.removed) {
    return { node: { ...node, first: firstResult.node }, removed: true, promotedPaneId: firstResult.promotedPaneId };
  }

  const secondResult = removePane(node.second, paneId);
  if (secondResult.removed) {
    return { node: { ...node, second: secondResult.node }, removed: true, promotedPaneId: secondResult.promotedPaneId };
  }

  return { node, removed: false };
}

export const useLayoutStore = defineStore("layout", {
  state: () => ({
    root: defaultLayout() as LayoutNode,
    activePaneId: "",
  }),
  getters: {
    activePane(state) {
      return state.activePaneId ? findPane(state.root, state.activePaneId) : null;
    },
    openPaths(state): string[] {
      const paths: string[] = [];
      const visit = (node: LayoutNode) => {
        if (node.type === "pane") {
          if (node.filePath) paths.push(node.filePath);
          return;
        }
        visit(node.first);
        visit(node.second);
      };
      visit(state.root);
      return paths;
    },
    openTerminalIds(state): string[] {
      const ids: string[] = [];
      const visit = (node: LayoutNode) => {
        if (node.type === "pane") {
          if (node.terminalId) ids.push(node.terminalId);
          return;
        }
        visit(node.first);
        visit(node.second);
      };
      visit(state.root);
      return ids;
    },
    openCodexSessionIds(state): string[] {
      const ids: string[] = [];
      const visit = (node: LayoutNode) => {
        if (node.type === "pane") {
          if (node.codexSessionId) ids.push(node.codexSessionId);
          return;
        }
        visit(node.first);
        visit(node.second);
      };
      visit(state.root);
      return ids;
    },
    openDiffPaths(state): string[] {
      const paths: string[] = [];
      const visit = (node: LayoutNode) => {
        if (node.type === "pane") {
          if (node.diffPath) paths.push(node.diffPath);
          return;
        }
        visit(node.first);
        visit(node.second);
      };
      visit(state.root);
      return paths;
    },
  },
  actions: {
    load() {
      const raw = localStorage.getItem(STORAGE_KEY);
      if (raw) {
        try {
          const parsed = JSON.parse(raw) as { root: LayoutNode; activePaneId?: string };
          this.root = parsed.root;
          this.activePaneId = parsed.activePaneId || firstPaneId(parsed.root);
          return;
        } catch {
          localStorage.removeItem(STORAGE_KEY);
        }
      }
      this.activePaneId = firstPaneId(this.root);
      this.save();
    },
    save() {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ root: this.root, activePaneId: this.activePaneId }));
    },
    snapshot() {
      return { root: cloneLayout(this.root), activePaneId: this.activePaneId || firstPaneId(this.root) };
    },
    restore(root: LayoutNode, activePaneId?: string | null) {
      this.root = cloneLayout(root);
      this.activePaneId = activePaneId && hasPane(this.root, activePaneId) ? activePaneId : firstPaneId(this.root);
      this.save();
    },
    reset() {
      this.root = defaultLayout();
      this.activePaneId = firstPaneId(this.root);
      this.save();
    },
    setActive(paneId: string) {
      this.activePaneId = paneId;
      this.save();
    },
    openFile(path: string) {
      if (!this.activePaneId) this.activePaneId = firstPaneId(this.root);
      this.root = mapNode(this.root, this.activePaneId, (pane) => ({
        ...pane,
        filePath: path,
        terminalId: undefined,
        codexSessionId: undefined,
        diffPath: undefined,
        diffCwd: undefined,
      }));
      this.save();
    },
    openFileInSplit(path: string, direction: SplitDirection) {
      if (!this.activePaneId) this.activePaneId = firstPaneId(this.root);
      const nextPaneId = id("pane");
      this.root = mapNode(this.root, this.activePaneId, (pane) => ({
        type: "split",
        id: id("split"),
        direction,
        ratio: 0.5,
        first: { ...pane },
        second: { type: "pane", id: nextPaneId, filePath: path },
      }));
      this.activePaneId = nextPaneId;
      this.save();
    },
    openTerminal(id: string) {
      if (!this.activePaneId) this.activePaneId = firstPaneId(this.root);
      this.root = mapNode(this.root, this.activePaneId, (pane) => ({
        ...pane,
        filePath: undefined,
        terminalId: id,
        codexSessionId: undefined,
        diffPath: undefined,
        diffCwd: undefined,
      }));
      this.save();
    },
    openCodexSession(id: string) {
      if (!this.activePaneId) this.activePaneId = firstPaneId(this.root);
      this.root = mapNode(this.root, this.activePaneId, (pane) => ({
        ...pane,
        filePath: undefined,
        terminalId: undefined,
        codexSessionId: id,
        diffPath: undefined,
        diffCwd: undefined,
      }));
      this.save();
    },
    openDiff(path: string, cwd = "") {
      if (!this.activePaneId) this.activePaneId = firstPaneId(this.root);
      this.root = mapNode(this.root, this.activePaneId, (pane) => ({
        ...pane,
        filePath: undefined,
        terminalId: undefined,
        codexSessionId: undefined,
        diffPath: path,
        diffCwd: cwd,
      }));
      this.save();
    },
    splitPane(paneId: string, direction: SplitDirection) {
      this.root = mapNode(this.root, paneId, (pane) => {
        const first = { ...pane };
        const second = { type: "pane" as const, id: id("pane") };
        return { type: "split", id: id("split"), direction, ratio: 0.5, first, second };
      });
      this.activePaneId = paneId;
      this.save();
    },
    setRatio(splitId: string, ratio: number) {
      const nextRatio = Math.max(0.12, Math.min(0.88, ratio));
      const apply = (node: LayoutNode): LayoutNode => {
        if (node.type === "pane") return node;
        if (node.id === splitId) return { ...node, ratio: nextRatio };
        return { ...node, first: apply(node.first), second: apply(node.second) };
      };
      this.root = apply(this.root);
      this.save();
    },
    clearPane(paneId: string) {
      this.root = mapNode(this.root, paneId, (pane) => ({
        ...pane,
        filePath: undefined,
        terminalId: undefined,
        codexSessionId: undefined,
        diffPath: undefined,
        diffCwd: undefined,
      }));
      this.save();
    },
    closePane(paneId: string) {
      if (this.root.type === "pane") {
        this.clearPane(paneId);
        return;
      }
      const result = removePane(this.root, paneId);
      if (!result.removed) return;
      this.root = result.node;
      this.activePaneId = result.promotedPaneId || firstPaneId(this.root);
      this.save();
    },
    clearTerminal(id: string) {
      this.root = mapAllPanes(this.root, (pane) => (pane.terminalId === id ? { ...pane, terminalId: undefined } : pane));
      this.save();
    },
    clearCodexSession(id: string) {
      this.root = mapAllPanes(this.root, (pane) => (pane.codexSessionId === id ? { ...pane, codexSessionId: undefined } : pane));
      this.save();
    },
  },
});
