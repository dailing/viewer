import { defineStore } from "pinia";
import {
  createAgentTask,
  dispatchAgentTask,
  dispatchReadyAgentTasks,
  getAgentTaskContext,
  listAgentTasks,
  patchAgentTask,
  patchAgentTaskDependencies,
  updateAgentTaskSettings,
} from "../api/client";
import type { AgentTask, AgentTaskContext, AgentTaskCreate, AgentTaskDependencyPatch, AgentTaskPatch, AgentTaskSettings } from "../types/agentTasks";

export const useAgentTasksStore = defineStore("agentTasks", {
  state: () => ({
    tasks: [] as AgentTask[],
    groups: [] as string[],
    selectedGroupId: "default",
    selectedId: "",
    settings: null as AgentTaskSettings | null,
    context: null as AgentTaskContext | null,
    loading: false,
  }),
  getters: {
    selectedTask(state) {
      return state.tasks.find((task) => task.id === state.selectedId) ?? null;
    },
    byId(state) {
      return Object.fromEntries(state.tasks.map((task) => [task.id, task])) as Record<string, AgentTask>;
    },
  },
  actions: {
    async load(groupId?: string) {
      const targetGroupId = groupId || this.selectedGroupId || "default";
      this.loading = true;
      try {
        const response = await listAgentTasks(targetGroupId);
        this.tasks = response.tasks;
        this.groups = response.groups.length ? response.groups : [targetGroupId];
        this.settings = response.settings;
        this.selectedGroupId = targetGroupId || response.settings.group_id || "default";
        if (!this.selectedId || !this.tasks.some((task) => task.id === this.selectedId)) {
          this.selectedId = this.tasks[0]?.id ?? "";
        }
      } finally {
        this.loading = false;
      }
    },
    async loadContext(id?: string) {
      const targetId = id || this.selectedId;
      if (!targetId) {
        this.context = null;
        return null;
      }
      this.context = await getAgentTaskContext(targetId);
      this.upsert(this.context.task);
      return this.context;
    },
    async create(task: AgentTaskCreate) {
      const created = await createAgentTask(task);
      this.upsert(created);
      if (!this.groups.includes(created.group_id)) this.groups = [...this.groups, created.group_id];
      this.selectedGroupId = created.group_id;
      this.selectedId = created.id;
      await this.loadContext(created.id);
      return created;
    },
    async patch(id: string, patch: AgentTaskPatch) {
      const updated = await patchAgentTask(id, patch);
      this.upsert(updated);
      if (id === this.selectedId) await this.loadContext(id);
      return updated;
    },
    async patchDependencies(id: string, patch: AgentTaskDependencyPatch) {
      const updated = await patchAgentTaskDependencies(id, patch);
      this.upsert(updated);
      if (id === this.selectedId) await this.loadContext(id);
      return updated;
    },
    async dispatch(id: string, force = false) {
      const updated = await dispatchAgentTask(id, force);
      this.upsert(updated);
      if (id === this.selectedId) await this.loadContext(id);
      return updated;
    },
    async dispatchReady(force = false) {
      const result = await dispatchReadyAgentTasks(this.selectedGroupId, force);
      for (const task of result.dispatched) this.upsert(task);
      return result;
    },
    async saveSettings(settings: Partial<AgentTaskSettings> & { default_group_id?: string }) {
      this.settings = await updateAgentTaskSettings(settings);
      return this.settings;
    },
    upsert(task: AgentTask) {
      const index = this.tasks.findIndex((item) => item.id === task.id);
      if (index === -1) {
        this.tasks = [task, ...this.tasks];
      } else {
        this.tasks = this.tasks.map((item) => (item.id === task.id ? task : item));
      }
    },
  },
});
