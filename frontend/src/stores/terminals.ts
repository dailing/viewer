import { defineStore } from "pinia";
import { createTerminal, deleteTerminal, listTerminals, terminateTerminal } from "../api/client";
import type { TerminalInfo } from "../types/terminals";

export const useTerminalsStore = defineStore("terminals", {
  state: () => ({
    terminals: [] as TerminalInfo[],
    loading: false,
  }),
  actions: {
    async load() {
      this.loading = true;
      try {
        this.terminals = await listTerminals();
      } finally {
        this.loading = false;
      }
    },
    async create() {
      const terminal = await createTerminal();
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
    },
  },
});
