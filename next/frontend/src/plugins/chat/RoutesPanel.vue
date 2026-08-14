<script setup lang="ts">
import { computed, inject, onMounted, reactive, ref } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { AgentCatalog, RoutingCandidate, RoutingConfig, RoutingPolicy } from "./types";
import { errorText } from "./types";

interface EditableCandidate extends Omit<RoutingCandidate, "parameters"> { parameters_text: string }
interface EditablePolicy extends Omit<RoutingPolicy, "candidates"> { candidates: EditableCandidate[] }

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("RoutesPanel requires PluginPaneHost");
const ctx: PluginCtx = injectedCtx;
const catalogs = ref<AgentCatalog[]>([]);
const state = reactive<{ default_routing_policy_id: string; routing_policies: EditablePolicy[] }>({ default_routing_policy_id: "", routing_policies: [] });
const error = ref("");
const saved = ref(false);
const onlineAgents = computed(() => catalogs.value.filter((item) => item.online));
const uid = () => Math.random().toString(36).slice(2, 10);

function providers(candidate: EditableCandidate) { return catalogs.value.find((item) => item.agent === candidate.agent_id)?.providers ?? []; }
function models(candidate: EditableCandidate) { return providers(candidate).find((item) => item.provider === candidate.provider_id)?.models ?? []; }
function syncCandidate(candidate: EditableCandidate, resetModel = false) {
  const availableProviders = providers(candidate);
  if (!availableProviders.some((item) => item.provider === candidate.provider_id)) candidate.provider_id = availableProviders[0]?.provider ?? "";
  if (resetModel && !models(candidate).includes(candidate.model_id)) candidate.model_id = models(candidate)[0] ?? "";
}
function changeAgent(candidate: EditableCandidate) { syncCandidate(candidate, true); }
function changeProvider(candidate: EditableCandidate) { if (candidate.model_id && !models(candidate).includes(candidate.model_id)) candidate.model_id = models(candidate)[0] ?? ""; }
function addPolicy() {
  const id = `policy-${uid()}`;
  state.routing_policies.push({ id, name: "New policy", enabled: true, auto_failover: false, max_attempts: 1, candidates: [] });
  if (!state.default_routing_policy_id) state.default_routing_policy_id = id;
}
function removePolicy(index: number) {
  const [removed] = state.routing_policies.splice(index, 1);
  if (removed && state.default_routing_policy_id === removed.id) state.default_routing_policy_id = state.routing_policies[0]?.id ?? "";
}
function addCandidate(policy: EditablePolicy) {
  const agent = onlineAgents.value[0] ?? catalogs.value[0];
  const provider = agent?.providers[0];
  policy.candidates.push({ id: `candidate-${uid()}`, name: "Candidate", agent_id: agent?.agent ?? "", provider_id: provider?.provider ?? "", model_id: provider?.models[0] ?? "", enabled: true, parameters_text: "{}" });
}
async function load() {
  const [routing, loadedCatalogs] = await Promise.all([
    ctx.bus.request("chat:_:routing:get", {}) as Promise<RoutingConfig>,
    ctx.bus.request("chat:_:agent-catalog", {}) as Promise<AgentCatalog[]>,
  ]);
  catalogs.value = loadedCatalogs;
  state.default_routing_policy_id = routing.default_routing_policy_id;
  state.routing_policies = routing.routing_policies.map((policy) => ({ ...policy, candidates: policy.candidates.map((candidate) => ({ ...candidate, parameters_text: JSON.stringify(candidate.parameters ?? {}, null, 2) })) }));
}
async function save() {
  saved.value = false;
  try {
    const value: RoutingConfig = { default_routing_policy_id: state.default_routing_policy_id, routing_policies: state.routing_policies.map((policy) => ({ ...policy, candidates: policy.candidates.map(({ parameters_text, ...candidate }) => ({ ...candidate, parameters: JSON.parse(parameters_text) as Record<string, unknown> })) })) };
    const result = await ctx.bus.request("chat:_:routing:put", value) as RoutingConfig;
    state.default_routing_policy_id = result.default_routing_policy_id;
    state.routing_policies = result.routing_policies.map((policy) => ({ ...policy, candidates: policy.candidates.map((candidate) => ({ ...candidate, parameters_text: JSON.stringify(candidate.parameters ?? {}, null, 2) })) }));
    error.value = "";
    saved.value = true;
  } catch (cause) { error.value = errorText(cause); }
}
onMounted(() => void load().catch((cause) => error.value = errorText(cause)));
</script>

<template>
  <section class="h-100 overflow-auto p-2">
    <div class="d-flex align-items-center mb-1"><h6 class="m-0 me-auto">Routing policies</h6><span v-if="saved" class="small text-success me-2">Saved</span><button class="btn btn-sm btn-outline-secondary me-1" title="Add policy" @click="addPolicy"><i class="bi-plus-lg" /></button><button class="btn btn-sm btn-primary" title="Validate and save" @click="save"><i class="bi-check-lg" /></button></div>
    <div v-if="error" class="small text-danger mb-1">{{ error }}</div>
    <label class="form-label small mb-1">Default policy</label>
    <select v-model="state.default_routing_policy_id" class="form-select form-select-sm mb-2"><option value="">None</option><option v-for="policy in state.routing_policies" :key="policy.id" :value="policy.id">{{ policy.name }}</option></select>
    <article v-for="(policy, policyIndex) in state.routing_policies" :key="policy.id" class="border-top pt-2 mb-3">
      <div class="d-flex gap-1 align-items-center mb-1"><input v-model="policy.name" class="form-control form-control-sm" placeholder="Policy name"><button class="btn btn-sm text-danger" title="Delete policy" @click="removePolicy(policyIndex)"><i class="bi-trash" /></button></div>
      <div class="row g-1 mb-2">
        <div class="col-6"><input v-model="policy.id" class="form-control form-control-sm font-monospace" placeholder="Policy id"></div>
        <div class="col-6"><input v-model.number="policy.max_attempts" type="number" min="0" class="form-control form-control-sm" placeholder="Max attempts"></div>
        <div class="col-6 form-check ms-2"><input :id="`${policy.id}-enabled`" v-model="policy.enabled" class="form-check-input" type="checkbox"><label class="form-check-label small" :for="`${policy.id}-enabled`">Enabled</label></div>
        <div class="col-5 form-check"><input :id="`${policy.id}-failover`" v-model="policy.auto_failover" class="form-check-input" type="checkbox"><label class="form-check-label small" :for="`${policy.id}-failover`">Auto failover</label></div>
      </div>
      <div class="d-flex align-items-center mb-1"><span class="small text-secondary me-auto">Candidates</span><button class="btn btn-sm btn-outline-secondary" title="Add candidate" @click="addCandidate(policy)"><i class="bi-plus-lg" /></button></div>
      <div v-for="(candidate, candidateIndex) in policy.candidates" :key="candidate.id" class="bg-body-tertiary p-1 mb-1">
        <div class="d-flex gap-1 mb-1"><input v-model="candidate.name" class="form-control form-control-sm" placeholder="Candidate name"><button class="btn btn-sm text-danger" title="Delete candidate" @click="policy.candidates.splice(candidateIndex, 1)"><i class="bi-trash" /></button></div>
        <div class="row g-1">
          <div class="col-4"><select v-model="candidate.agent_id" class="form-select form-select-sm" @change="changeAgent(candidate)"><option v-for="item in catalogs" :key="item.agent" :value="item.agent" :disabled="!item.online">{{ item.agent }}{{ item.online ? "" : " (offline)" }}</option></select></div>
          <div class="col-4"><select v-model="candidate.provider_id" class="form-select form-select-sm" @change="changeProvider(candidate)"><option v-for="item in providers(candidate)" :key="item.provider" :value="item.provider">{{ item.provider }}</option></select></div>
          <div class="col-4"><select v-model="candidate.model_id" class="form-select form-select-sm"><option value="">Default</option><option v-if="candidate.model_id && !models(candidate).includes(candidate.model_id)" :value="candidate.model_id">{{ candidate.model_id }}</option><option v-for="model in models(candidate)" :key="model" :value="model">{{ model }}</option></select></div>
          <div class="col-10"><textarea v-model="candidate.parameters_text" class="form-control form-control-sm font-monospace" rows="2" placeholder="Parameters JSON" /></div>
          <div class="col-2 form-check d-flex justify-content-center align-items-center"><input v-model="candidate.enabled" class="form-check-input" type="checkbox" title="Enabled"></div>
        </div>
      </div>
    </article>
  </section>
</template>
