import { defineStore } from "pinia";
import type { LayoutNode, PaneContent, SplitDirection } from "../types/layout";
import { namespacedStorageKey } from "../utils/userProfile";

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
  return normalizeLayoutNode(JSON.parse(JSON.stringify(node)) as LayoutNode);
}

function normalizeLayoutNode(node: LayoutNode): LayoutNode {
  if (node.type === "pane") {
    const { filePath, terminalId, diffPath, diffCwd, chatId, history } = node;
    return { type: "pane", id: node.id, filePath, terminalId, diffPath, diffCwd, chatId, history: normalizeHistory(history) };
  }
  return { ...node, first: normalizeLayoutNode(node.first), second: normalizeLayoutNode(node.second) };
}

function normalizeHistory(history: PaneContent[] | undefined): PaneContent[] | undefined {
  const next = (history ?? []).filter(hasContent).slice(-50);
  return next.length ? next : undefined;
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

function paneContent(pane: Extract<LayoutNode, { type: "pane" }>): PaneContent {
  return {
    filePath: pane.filePath,
    terminalId: pane.terminalId,
    diffPath: pane.diffPath,
    diffCwd: pane.diffCwd,
    chatId: pane.chatId,
  };
}

function hasContent(content: PaneContent | null | undefined): boolean {
  return Boolean(content?.filePath || content?.terminalId || content?.diffPath || content?.chatId);
}

function sameContent(left: PaneContent, right: PaneContent): boolean {
  return (
    (left.filePath ?? "") === (right.filePath ?? "") &&
    (left.terminalId ?? "") === (right.terminalId ?? "") &&
    (left.diffPath ?? "") === (right.diffPath ?? "") &&
    (left.diffCwd ?? "") === (right.diffCwd ?? "") &&
    (left.chatId ?? "") === (right.chatId ?? "")
  );
}

function contentForFile(path: string): PaneContent {
  return { filePath: path };
}

function contentForTerminal(id: string): PaneContent {
  return { terminalId: id };
}

function contentForDiff(path: string, cwd = ""): PaneContent {
  return { diffPath: path, diffCwd: cwd };
}

function contentForChat(id: string): PaneContent {
  return { chatId: id };
}

function pushPaneHistory(pane: Extract<LayoutNode, { type: "pane" }>, nextContent: PaneContent): PaneContent[] | undefined {
  const current = paneContent(pane);
  const history = normalizeHistory(pane.history) ?? [];
  if (!hasContent(current) || sameContent(current, nextContent)) return history.length ? history : undefined;
  const previous = history[history.length - 1];
  const nextHistory = previous && sameContent(previous, current) ? history : [...history, current];
  return nextHistory.slice(-50);
}

function applyPaneContent(pane: Extract<LayoutNode, { type: "pane" }>, content: PaneContent): Extract<LayoutNode, { type: "pane" }> {
  return {
    ...pane,
    filePath: content.filePath,
    terminalId: content.terminalId,
    diffPath: content.diffPath,
    diffCwd: content.diffCwd,
    chatId: content.chatId,
  };
}

function replacePaneContent(pane: Extract<LayoutNode, { type: "pane" }>, nextContent: PaneContent): Extract<LayoutNode, { type: "pane" }> {
  return {
    ...applyPaneContent(pane, nextContent),
    history: pushPaneHistory(pane, nextContent),
  };
}

function emitPaneBeforeNavigate(paneId: string): void {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new CustomEvent("viewer:pane-before-navigate", { detail: { paneId } }));
}

export const useLayoutStore = defineStore("layout", {
  state: () => ({
    root: defaultLayout() as LayoutNode,
    activePaneId: "",
    storageScope: "",
  }),
  getters: {
    activePane(state) {
      return state.activePaneId ? findPane(state.root, state.activePaneId) : null;
    },
    activePaneCanGoBack(state): boolean {
      const pane = state.activePaneId ? findPane(state.root, state.activePaneId) : null;
      return Boolean(pane?.type === "pane" && normalizeHistory(pane.history)?.length);
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
    openChatIds(state): string[] {
      const ids: string[] = [];
      const visit = (node: LayoutNode) => {
        if (node.type === "pane") {
          if (node.chatId) ids.push(node.chatId);
          return;
        }
        visit(node.first);
        visit(node.second);
      };
      visit(state.root);
      return ids;
    },
  },
  actions: {
    storageKey() {
      return this.storageScope ? `${STORAGE_KEY}.${this.storageScope}` : STORAGE_KEY;
    },
    setStorageScope(scope = "") {
      this.storageScope = scope;
    },
    load(scope?: string) {
      if (scope !== undefined) this.storageScope = scope;
      const key = namespacedStorageKey(this.storageKey());
      const raw = localStorage.getItem(key);
      if (raw) {
        try {
          const parsed = JSON.parse(raw) as { root: LayoutNode; activePaneId?: string };
          this.root = normalizeLayoutNode(parsed.root);
          this.activePaneId = parsed.activePaneId || firstPaneId(this.root);
          return;
        } catch {
          localStorage.removeItem(key);
        }
      }
      this.activePaneId = firstPaneId(this.root);
      this.save();
    },
    save() {
      localStorage.setItem(namespacedStorageKey(this.storageKey()), JSON.stringify({ root: this.root, activePaneId: this.activePaneId }));
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
      emitPaneBeforeNavigate(this.activePaneId);
      this.root = mapNode(this.root, this.activePaneId, (pane) => replacePaneContent(pane, contentForFile(path)));
      this.save();
    },
    openTerminal(id: string) {
      if (!this.activePaneId) this.activePaneId = firstPaneId(this.root);
      emitPaneBeforeNavigate(this.activePaneId);
      this.root = mapNode(this.root, this.activePaneId, (pane) => replacePaneContent(pane, contentForTerminal(id)));
      this.save();
    },
    openDiff(path: string, cwd = "") {
      if (!this.activePaneId) this.activePaneId = firstPaneId(this.root);
      emitPaneBeforeNavigate(this.activePaneId);
      this.root = mapNode(this.root, this.activePaneId, (pane) => replacePaneContent(pane, contentForDiff(path, cwd)));
      this.save();
    },
    openChat(chatId: string) {
      if (!this.activePaneId) this.activePaneId = firstPaneId(this.root);
      emitPaneBeforeNavigate(this.activePaneId);
      this.root = mapNode(this.root, this.activePaneId, (pane) => replacePaneContent(pane, contentForChat(chatId)));
      this.save();
    },
    goBack(paneId?: string) {
      const targetPaneId = paneId ?? this.activePaneId;
      if (!targetPaneId) return;
      emitPaneBeforeNavigate(targetPaneId);
      this.root = mapNode(this.root, targetPaneId, (pane) => {
        const history = normalizeHistory(pane.history) ?? [];
        const previous = history[history.length - 1];
        if (!previous) return pane;
        return {
          ...applyPaneContent(pane, previous),
          history: history.slice(0, -1),
        };
      });
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
      emitPaneBeforeNavigate(paneId);
      this.root = mapNode(this.root, paneId, (pane) => ({
        ...pane,
        filePath: undefined,
        terminalId: undefined,
        diffPath: undefined,
        diffCwd: undefined,
        chatId: undefined,
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
  },
});
