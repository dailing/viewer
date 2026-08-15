<script setup lang="ts">
import { computed, inject, onMounted, reactive, ref } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { AgentCatalog, RoutingCandidate, RoutingConfig, RoutingPolicy } from "../chat/types";
import { errorText } from "../chat/types";
import MasterDetail from "./MasterDetail.vue";
import TextSelect from "./TextSelect.vue";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("RoutesPanel requires PluginPaneHost");
const ctx = injectedCtx;
const catalogs = ref<AgentCatalog[]>([]);
const state = reactive<RoutingConfig>({ default_routing_policy_id: "", routing_policies: [] });
const selectedID = ref("");
const error = ref("");
const saved = ref(false);
const draggedIndex = ref<number | null>(null);
const dragInsertIndex = ref<number | null>(null);
const onlineAgents = computed(() => catalogs.value.filter((item) => item.online));
const items = computed(() => state.routing_policies.map(({ id, name }) => ({ id, name })));
const selected = computed(() => state.routing_policies.find((policy) => policy.id === selectedID.value) ?? null);
const selectedJSON = computed(() => selected.value === null ? "" : JSON.stringify(selected.value, null, 2));
const uid = (): string => Math.random().toString(36).slice(2, 10);

function providers(candidate: RoutingCandidate) {
  return catalogs.value.find((item) => item.agent === candidate.agent_id)?.providers ?? [];
}

function models(candidate: RoutingCandidate) {
  return providers(candidate).find((item) => item.provider === candidate.provider_id)?.models ?? [];
}

function agentOptions() {
  return catalogs.value.map((item) => ({ value: item.agent, label: `${item.agent}${item.online ? "" : " (offline)"}`, disabled: !item.online }));
}

function providerOptions(candidate: RoutingCandidate) {
  return providers(candidate).map((item) => ({ value: item.provider, label: item.provider }));
}

function modelOptions(candidate: RoutingCandidate) {
  return [{ value: "", label: "Default" }, ...models(candidate).map((model) => ({ value: model, label: model }))];
}

function changeAgent(candidate: RoutingCandidate, agentID: string): void {
  candidate.agent_id = agentID;
  candidate.provider_id = providers(candidate)[0]?.provider ?? "";
  candidate.model_id = models(candidate)[0] ?? "";
}

function changeProvider(candidate: RoutingCandidate, providerID: string): void {
  candidate.provider_id = providerID;
  if (!models(candidate).includes(candidate.model_id)) candidate.model_id = models(candidate)[0] ?? "";
}

function addPolicy(): void {
  const id = `policy-${uid()}`;
  state.routing_policies.push({ id, name: "New policy", enabled: true, auto_failover: false, max_attempts: 1, candidates: [] });
  if (!state.default_routing_policy_id) state.default_routing_policy_id = id;
  selectedID.value = id;
  saved.value = false;
}

function removePolicy(): void {
  const policy = selected.value;
  if (policy === null || !confirm(`Delete policy ${policy.name}?`)) return;
  const index = state.routing_policies.findIndex((item) => item.id === policy.id);
  if (index < 0) return;
  state.routing_policies.splice(index, 1);
  if (state.default_routing_policy_id === policy.id) state.default_routing_policy_id = state.routing_policies[0]?.id ?? "";
  selectedID.value = state.routing_policies[index]?.id ?? state.routing_policies[index - 1]?.id ?? "";
  saved.value = false;
}

function renamePolicyID(policy: RoutingPolicy, event: Event): void {
  const oldID = policy.id;
  const nextID = (event.target as HTMLInputElement).value;
  policy.id = nextID;
  selectedID.value = nextID;
  if (state.default_routing_policy_id === oldID) state.default_routing_policy_id = nextID;
  saved.value = false;
}

function setDefault(policy: RoutingPolicy, event: Event): void {
  state.default_routing_policy_id = (event.target as HTMLInputElement).checked ? policy.id : "";
  saved.value = false;
}

function newCandidate(): RoutingCandidate {
  const agent = onlineAgents.value[0];
  const provider = agent?.providers[0];
  return {
    id: `candidate-${uid()}`,
    name: "Candidate",
    agent_id: agent?.agent ?? "",
    provider_id: provider?.provider ?? "",
    model_id: provider?.models[0] ?? "",
    enabled: true,
    parameters: {},
  };
}

function addCandidateAt(policy: RoutingPolicy, index: number): void {
  policy.candidates.splice(index, 0, newCandidate());
  saved.value = false;
}

function removeCandidate(policy: RoutingPolicy, index: number): void {
  if (!confirm("Delete candidate?")) return;
  policy.candidates.splice(index, 1);
  saved.value = false;
}

function dragStart(event: DragEvent, index: number): void {
  draggedIndex.value = index;
  dragInsertIndex.value = index;
  event.dataTransfer?.setData("text/plain", String(index));
  if (event.dataTransfer !== null) event.dataTransfer.effectAllowed = "move";
}

function dragOverRow(event: DragEvent, index: number): void {
  const element = event.currentTarget as HTMLElement;
  dragInsertIndex.value = event.clientY < element.getBoundingClientRect().top + element.offsetHeight / 2 ? index : index + 1;
}

function dropCandidate(policy: RoutingPolicy, targetIndex = dragInsertIndex.value): void {
  const sourceIndex = draggedIndex.value;
  if (sourceIndex === null || targetIndex === null) return;
  const [candidate] = policy.candidates.splice(sourceIndex, 1);
  if (candidate !== undefined) {
    const adjustedIndex = sourceIndex < targetIndex ? targetIndex - 1 : targetIndex;
    policy.candidates.splice(adjustedIndex, 0, candidate);
  }
  endDrag();
  saved.value = false;
}

function endDrag(): void {
  draggedIndex.value = null;
  dragInsertIndex.value = null;
}

async function load(): Promise<void> {
  const [routing, loadedCatalogs] = await Promise.all([
    ctx.bus.request("chat:_:routing:get", {}) as Promise<RoutingConfig>,
    ctx.bus.request("chat:_:agent-catalog", {}) as Promise<AgentCatalog[]>,
  ]);
  catalogs.value = loadedCatalogs;
  state.default_routing_policy_id = routing.default_routing_policy_id;
  state.routing_policies = routing.routing_policies;
  if (!state.routing_policies.some((policy) => policy.id === selectedID.value)) selectedID.value = state.routing_policies[0]?.id ?? "";
}

async function save(): Promise<void> {
  saved.value = false;
  try {
    const value: RoutingConfig = {
      default_routing_policy_id: state.default_routing_policy_id,
      routing_policies: state.routing_policies.map((policy) => ({
        ...policy,
        candidates: policy.candidates.map((candidate) => ({ ...candidate, parameters: candidate.parameters })),
      })),
    };
    const result = await ctx.bus.request("chat:_:routing:put", value) as RoutingConfig;
    state.default_routing_policy_id = result.default_routing_policy_id;
    state.routing_policies = result.routing_policies;
    if (!state.routing_policies.some((policy) => policy.id === selectedID.value)) selectedID.value = state.routing_policies[0]?.id ?? "";
    error.value = "";
    saved.value = true;
  } catch (cause) {
    error.value = errorText(cause);
  }
}

onMounted(() => void load().catch((cause) => { error.value = errorText(cause); }));
</script>

<template>
  <MasterDetail :items="items" :selected-id="selectedID" create-label="＋ 新建 Policy" @select="selectedID = $event" @create="addPolicy">
    <template #detail>
      <div v-if="error" class="small text-danger mb-2">{{ error }}</div>
      <div v-if="selected" class="route-config">
        <div class="row g-2 align-items-end">
          <label class="col-lg-4 form-label small">Name<input v-model="selected.name" class="form-control form-control-sm mt-1" @input="saved = false"></label>
          <label class="col-lg-3 form-label small">ID<input :value="selected.id" class="form-control form-control-sm font-monospace mt-1" @input="renamePolicyID(selected, $event)"></label>
          <label class="col-lg-2 form-label small">Max attempts<input v-model.number="selected.max_attempts" type="number" min="1" class="form-control form-control-sm mt-1" @input="saved = false"></label>
          <div class="col-lg-3 d-flex flex-wrap gap-3 pb-1">
            <label class="form-check small"><input v-model="selected.auto_failover" class="form-check-input" type="checkbox" @change="saved = false">Auto failover</label>
            <label class="form-check small"><input v-model="selected.enabled" class="form-check-input" type="checkbox" @change="saved = false">Enabled</label>
            <label class="form-check form-switch small"><input class="form-check-input" type="checkbox" :checked="state.default_routing_policy_id === selected.id" @change="setDefault(selected, $event)">设为默认</label>
          </div>
        </div>

        <div class="candidate-heading">Candidates（按顺序尝试）</div>
        <div v-if="selected.candidates.length === 0" class="empty-candidate" @click="addCandidateAt(selected, 0)">＋ 添加候选</div>
        <template v-for="(candidate, candidateIndex) in selected.candidates" :key="candidate.id">
          <div
            class="candidate-row"
            :class="{ 'drop-before': dragInsertIndex === candidateIndex, 'drop-after': dragInsertIndex === candidateIndex + 1 }"
            draggable="true"
            @dragstart="dragStart($event, candidateIndex)"
            @dragover.prevent="dragOverRow($event, candidateIndex)"
            @drop.prevent="dropCandidate(selected)"
            @dragend="endDrag"
          >
            <i class="bi bi-grip-vertical candidate-grip" title="拖拽排序"></i>
            <TextSelect :model-value="candidate.agent_id" label="Agent" :options="agentOptions()" @update:model-value="changeAgent(candidate, $event)" />
            <TextSelect :model-value="candidate.provider_id" label="Provider" :options="providerOptions(candidate)" @update:model-value="changeProvider(candidate, $event)" />
            <TextSelect v-model="candidate.model_id" label="Model" :options="modelOptions(candidate)" />
            <label class="candidate-enabled small"><input v-model="candidate.enabled" type="checkbox" class="form-check-input">Enabled</label>
            <button type="button" class="candidate-remove" title="删除候选" @click="removeCandidate(selected, candidateIndex)"><i class="bi bi-trash"></i></button>
          </div>
          <div
            class="candidate-divider"
            :class="{ active: dragInsertIndex === candidateIndex + 1 }"
            @dragover.prevent="dragInsertIndex = candidateIndex + 1"
            @drop.prevent="dropCandidate(selected, candidateIndex + 1)"
          >
            <button type="button" title="在此处添加候选" @click="addCandidateAt(selected, candidateIndex + 1)">+</button>
          </div>
        </template>

        <div class="small text-secondary mt-3 mb-1">Policy JSON</div>
        <pre class="policy-json">{{ selectedJSON }}</pre>

        <div class="d-flex align-items-center gap-1 mt-3">
          <button class="btn btn-sm btn-primary" @click="save"><i class="bi bi-check-lg me-1"></i>保存</button>
          <span v-if="saved" class="small text-success">Saved</span>
          <button class="btn btn-sm btn-outline-danger ms-auto" @click="removePolicy"><i class="bi bi-trash me-1"></i>删除该 Policy</button>
        </div>
      </div>
      <div v-else-if="!error" class="small text-secondary">选择或新建 Policy。</div>
    </template>
  </MasterDetail>
</template>

<style scoped>
.candidate-heading {
  color: var(--color-text-muted);
  font-size: var(--font-size-ui-small);
  margin-top: 18px;
}

.candidate-row {
  align-items: center;
  background: var(--color-surface-muted);
  display: grid;
  gap: 10px;
  grid-template-columns: 16px minmax(100px, 1fr) minmax(100px, 1fr) minmax(100px, 1fr) auto 24px;
  min-height: 52px;
  padding: 7px 8px;
  position: relative;
}

.candidate-row.drop-before::before,
.candidate-row.drop-after::after {
  border-top: 2px solid var(--color-accent);
  content: "";
  left: 0;
  position: absolute;
  right: 0;
}

.candidate-row.drop-before::before { top: 0; }
.candidate-row.drop-after::after { bottom: 0; }

.candidate-grip {
  color: var(--color-text-subtle);
  cursor: grab;
}

.candidate-enabled {
  align-items: center;
  display: flex;
  gap: 5px;
  margin: 0;
}

.candidate-remove {
  background: transparent;
  border: 0;
  color: var(--color-text-subtle);
  opacity: 0;
  padding: 3px;
}

.candidate-row:hover .candidate-remove,
.candidate-remove:focus-visible {
  opacity: 1;
}

.candidate-remove:hover {
  color: var(--color-danger);
}

.candidate-divider {
  align-items: center;
  border-top: 1px dashed var(--color-border-strong);
  display: flex;
  height: 15px;
  justify-content: center;
}

.candidate-divider.active {
  border-color: var(--color-accent);
}

.candidate-divider button {
  align-items: center;
  background: var(--color-surface);
  border: 1px solid var(--color-border-strong);
  border-radius: 50%;
  color: var(--color-text-muted);
  display: flex;
  font-size: 13px;
  height: 17px;
  justify-content: center;
  line-height: 1;
  padding: 0 0 1px;
  width: 17px;
}

.candidate-divider button:hover {
  border-color: var(--color-accent);
  color: var(--color-accent);
}

.empty-candidate {
  border-top: 1px dashed var(--color-border-strong);
  color: var(--color-text-muted);
  cursor: pointer;
  margin-top: 6px;
  padding: 12px;
  text-align: center;
}

.policy-json {
  background: var(--color-surface-muted);
  border: 1px solid var(--color-border);
  color: var(--color-text-muted);
  font-size: var(--font-size-ui-small);
  margin: 0;
  max-height: 200px;
  overflow: auto;
  padding: 8px;
  white-space: pre-wrap;
}

@media (max-width: 760px) {
  .candidate-row {
    grid-template-columns: 16px minmax(0, 1fr) 24px;
  }

  .candidate-row :deep(.text-select) {
    grid-column: 2;
  }

  .candidate-enabled {
    grid-column: 2;
  }
}
</style>
