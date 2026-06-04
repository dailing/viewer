import { defineStore } from "pinia";
import {
  createAgentLoop,
  deleteAgentLoop,
  getAgentLoopRun,
  listAgentLoopRuns,
  listAgentLoops,
  pauseAgentLoop,
  reloadAgentLoops,
  resetAgentLoopSession,
  resumeAgentLoop,
  runAgentLoop,
  updateAgentLoop,
} from "../api/client";
import type { AgentLoopDefinition, AgentLoopInfo, AgentLoopRunRecord } from "../types/agentLoops";

export const useAgentLoopsStore = defineStore("agentLoops", {
  state: () => ({
    tasks: [] as AgentLoopInfo[],
    runsByTask: {} as Record<string, AgentLoopRunRecord[]>,
    runDetails: {} as Record<string, AgentLoopRunRecord>,
    selectedId: "" as string,
    loading: false,
  }),
  actions: {
    async load() {
      this.loading = true;
      try {
        this.tasks = await listAgentLoops();
        if (!this.selectedId && this.tasks.length) this.selectedId = this.tasks[0].definition.id;
      } finally {
        this.loading = false;
      }
    },
    async reload() {
      this.tasks = await reloadAgentLoops();
      if (!this.tasks.some((task) => task.definition.id === this.selectedId)) {
        this.selectedId = this.tasks[0]?.definition.id ?? "";
      }
    },
    async create(name: string) {
      const task = await createAgentLoop(name);
      this.upsert(task);
      this.selectedId = task.definition.id;
      return task;
    },
    async save(definition: AgentLoopDefinition) {
      const task = await updateAgentLoop(definition.id, definition);
      this.upsert(task);
      return task;
    },
    async remove(id: string) {
      await deleteAgentLoop(id);
      this.tasks = this.tasks.filter((task) => task.definition.id !== id);
      delete this.runsByTask[id];
      if (this.selectedId === id) this.selectedId = this.tasks[0]?.definition.id ?? "";
    },
    async runNow(id: string) {
      const task = await runAgentLoop(id);
      this.upsert(task);
      return task;
    },
    async pause(id: string) {
      const task = await pauseAgentLoop(id);
      this.upsert(task);
      return task;
    },
    async resume(id: string) {
      const task = await resumeAgentLoop(id);
      this.upsert(task);
      return task;
    },
    async resetSession(id: string) {
      const task = await resetAgentLoopSession(id);
      this.upsert(task);
      return task;
    },
    async loadRuns(id: string) {
      this.runsByTask[id] = await listAgentLoopRuns(id);
      return this.runsByTask[id];
    },
    async loadRunDetail(id: string, runId: string) {
      const detail = await getAgentLoopRun(id, runId);
      this.runDetails[`${id}:${runId}`] = detail;
      return detail;
    },
    upsert(task: AgentLoopInfo) {
      const index = this.tasks.findIndex((item) => item.definition.id === task.definition.id);
      if (index === -1) {
        this.tasks = [task, ...this.tasks];
      } else {
        this.tasks = this.tasks.map((item) => (item.definition.id === task.definition.id ? task : item));
      }
    },
  },
});
