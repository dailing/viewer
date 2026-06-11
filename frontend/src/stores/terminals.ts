import { defineStore } from "pinia";
import { createTerminal, deleteTerminal, listTerminals, terminateTerminal } from "../api/client";
import type { TerminalInfo } from "../types/terminals";
import { namespacedStorageKey } from "../utils/userProfile";

const PINNED_TERMINALS_KEY = "viewer.pinnedTerminals.v1";

function readPinnedTerminals(): string[] {
  try {
    const raw = localStorage.getItem(namespacedStorageKey(PINNED_TERMINALS_KEY));
    const parsed = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) return [];
    const seen = new Set<string>();
    return parsed
      .filter((item): item is string => typeof item === "string" && item.trim().length > 0)
      .filter((item) => {
        if (seen.has(item)) return false;
        seen.add(item);
        return true;
      });
  } catch {
    return [];
  }
}

function writePinnedTerminals(ids: string[]) {
  localStorage.setItem(namespacedStorageKey(PINNED_TERMINALS_KEY), JSON.stringify(ids));
}

export const useTerminalsStore = defineStore("terminals", {
  state: () => ({
    terminals: [] as TerminalInfo[],
    pinned: readPinnedTerminals(),
    loading: false,
  }),
  getters: {
    pinnedTerminals(state): TerminalInfo[] {
      const byId = new Map(state.terminals.map((terminal) => [terminal.id, terminal]));
      return state.pinned.map((id) => byId.get(id)).filter((terminal): terminal is TerminalInfo => Boolean(terminal));
    },
    isPinned:
      (state) =>
      (id: string): boolean =>
        state.pinned.includes(id),
  },
  actions: {
    async load() {
      if (this.loading) return;
      this.loading = true;
      try {
        this.terminals = await listTerminals();
      } finally {
        this.loading = false;
      }
    },
    async create(cwd = "") {
      const terminal = await createTerminal(cwd);
      this.terminals = [...this.terminals, terminal];
      return terminal;
    },
    upsert(terminal: TerminalInfo) {
      const index = this.terminals.findIndex((item) => item.id === terminal.id);
      if (index === -1) {
        this.terminals = [...this.terminals, terminal];
        return;
      }
      this.terminals = this.terminals.map((item) => (item.id === terminal.id ? terminal : item));
    },
    async terminate(id: string) {
      this.upsert(await terminateTerminal(id));
    },
    async remove(id: string) {
      await deleteTerminal(id);
      this.terminals = this.terminals.filter((item) => item.id !== id);
      this.pinned = this.pinned.filter((item) => item !== id);
      writePinnedTerminals(this.pinned);
    },
    togglePin(id: string) {
      if (this.pinned.includes(id)) {
        this.pinned = this.pinned.filter((item) => item !== id);
      } else {
        this.pinned = [id, ...this.pinned];
      }
      writePinnedTerminals(this.pinned);
    },
  },
});
