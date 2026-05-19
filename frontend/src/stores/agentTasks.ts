import { defineStore } from "pinia";
import {
  createAgentTask,
  deleteAgentTask,
  dispatchAgentTask,
  dispatchReadyAgentTasks,
  getAgentTaskContext,
  getAgentTaskPlan,
  listAgentTaskGroups,
  listAgentTasks,
  patchAgentTask,
  patchAgentTaskDependencies,
  requestAgentTaskManager,
  resetAgentTask,
  updateAgentTaskPlan,
  updateAgentTaskSettings,
} from "../api/client";
import type { AgentTask, AgentTaskContext, AgentTaskCreate, AgentTaskDependencyPatch, AgentTaskGroup, AgentTaskManagerRequest, AgentTaskPatch, AgentTaskPlan, AgentTaskResetAction, AgentTaskResetResponse, AgentTaskSettings } from "../types/agentTasks";

export const useAgentTasksStore = defineStore("agentTasks", {
  state: () => ({
    tasks: [] as AgentTask[],
    groupItems: [] as AgentTaskGroup[],
    groups: [] as string[],
    selectedGroupId: "default",
    selectedId: "",
    settings: null as AgentTaskSettings | null,
    plan: null as AgentTaskPlan | null,
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
        await this.loadGroups();
        this.settings = response.settings;
        this.selectedGroupId = targetGroupId || response.settings.group_id || "default";
        this.plan = await getAgentTaskPlan(this.selectedGroupId);
        if (!this.selectedId || !this.tasks.some((task) => task.id === this.selectedId)) {
          this.selectedId = this.tasks[0]?.id ?? "";
        }
      } finally {
        this.loading = false;
      }
    },
    async loadGroups() {
      this.groupItems = await listAgentTaskGroups();
      this.groups = this.groupItems.map((group) => group.group_id);
      if (!this.groups.includes(this.selectedGroupId)) this.groups = [this.selectedGroupId, ...this.groups];
      return this.groupItems;
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
    async remove(id: string) {
      await deleteAgentTask(id);
      this.tasks = this.tasks.filter((task) => task.id !== id);
      if (this.selectedId === id) {
        this.selectedId = this.tasks[0]?.id ?? "";
        this.context = null;
        if (this.selectedId) await this.loadContext(this.selectedId);
      }
      await this.loadGroups();
    },
    async dispatch(id: string, force = false) {
      const updated = await dispatchAgentTask(id, force);
      this.upsert(updated);
      if (id === this.selectedId) await this.loadContext(id);
      return updated;
    },
    async reset(id: string, action: AgentTaskResetAction, reason: string): Promise<AgentTaskResetResponse> {
      const result = await resetAgentTask(id, action, reason);
      for (const task of result.tasks) this.upsert(task);
      if (result.dispatched) this.upsert(result.dispatched);
      await this.load(this.selectedGroupId);
      if (this.selectedId) await this.loadContext(this.selectedId);
      return result;
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
    async savePlan(plan: Pick<AgentTaskPlan, "group_id" | "goal" | "plan" | "context" | "constraints"> & { reason?: string }) {
      this.plan = await updateAgentTaskPlan(plan);
      return this.plan;
    },
    async requestManager(request: AgentTaskManagerRequest) {
      const result = await requestAgentTaskManager(request);
      await this.load(this.selectedGroupId);
      if (this.selectedId) await this.loadContext(this.selectedId);
      return result;
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
