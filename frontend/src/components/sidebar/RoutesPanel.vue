<script setup lang="ts">
import { computed, ref } from "vue";
import type { ProviderAccountConfig, RoutingPolicyConfig } from "../../types/files";
import type { AgentProvider } from "../../types/agents";
import type { RoutingConfigData } from "../../types/superWorkspace";

const props = defineProps<{
  policies: RoutingPolicyConfig[];
  accounts: ProviderAccountConfig[];
  defaultPolicyId: string;
  providers: { id: AgentProvider; name: string }[];
}>();

const emit = defineEmits<{ save: [config: RoutingConfigData] }>();
const selectedPolicyId = ref("");
const selectedPolicy = computed(() => props.policies.find((policy) => policy.id === selectedPolicyId.value) ?? null);
const defaultPolicyId = ref(props.defaultPolicyId);

function uid(prefix: string) {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`;
}

function addPolicy() {
  const id = uid("route");
  props.policies.push({
    id,
    name: "New Route",
    description: "",
    enabled: true,
    auto_failover: true,
    max_attempts: 3,
    cooldown_seconds: 600,
    candidates: [],
  });
  selectedPolicyId.value = id;
  if (!defaultPolicyId.value) defaultPolicyId.value = id;
}

function deletePolicy(policy: RoutingPolicyConfig) {
  if (!window.confirm(`Delete route "${policy.name}"?`)) return;
  const index = props.policies.findIndex((item) => item.id === policy.id);
  if (index >= 0) props.policies.splice(index, 1);
  if (defaultPolicyId.value === policy.id) defaultPolicyId.value = props.policies[0]?.id ?? "";
  selectedPolicyId.value = "";
}

function addCandidate(policy: RoutingPolicyConfig) {
  const runtime = props.providers[0]?.id ?? "codex";
  policy.candidates.push({
    id: uid("target"),
    name: "",
    runtime_id: runtime,
    provider_account_id: "",
    model_id: null,
    enabled: true,
    parameters: {},
  });
}

function moveCandidate(policy: RoutingPolicyConfig, index: number, delta: number) {
  const target = index + delta;
  if (target < 0 || target >= policy.candidates.length) return;
  const [candidate] = policy.candidates.splice(index, 1);
  policy.candidates.splice(target, 0, candidate);
}

function candidateCapability(candidate: RoutingPolicyConfig["candidates"][number], key: string) {
  const capabilities = candidate.parameters.capabilities;
  return Boolean(capabilities && typeof capabilities === "object" && !Array.isArray(capabilities) && (capabilities as Record<string, unknown>)[key]);
}

function setCandidateCapability(candidate: RoutingPolicyConfig["candidates"][number], key: string, value: boolean) {
  const current = candidate.parameters.capabilities;
  const capabilities = current && typeof current === "object" && !Array.isArray(current) ? { ...(current as Record<string, unknown>) } : {};
  capabilities[key] = value;
  candidate.parameters = { ...candidate.parameters, capabilities };
}

function candidateContextWindow(candidate: RoutingPolicyConfig["candidates"][number]) {
  const value = candidate.parameters.context_window;
  return typeof value === "number" ? value : 0;
}

function setCandidateContextWindow(candidate: RoutingPolicyConfig["candidates"][number], event: Event) {
  const value = Number((event.target as HTMLInputElement).value || 0);
  candidate.parameters = { ...candidate.parameters, context_window: value > 0 ? value : undefined };
}

function addAccount() {
  props.accounts.push({
    id: uid("account"),
    name: "New Account",
    provider: "",
    credential_ref: "",
    enabled: true,
    monthly_budget: null,
  });
}

function save() {
  emit("save", {
    default_routing_policy_id: defaultPolicyId.value,
    provider_accounts: props.accounts,
    routing_policies: props.policies,
  });
}
</script>

<template>
  <div class="sidebar-panel routes-panel">
    <div class="route-toolbar">
      <label class="field">
        <span>Workspace Default</span>
        <select v-model="defaultPolicyId" class="form-select form-select-sm">
          <option v-for="policy in props.policies" :key="policy.id" :value="policy.id">{{ policy.name }}</option>
        </select>
      </label>
      <button class="btn btn-sm btn-primary" type="button" @click="save"><i class="bi bi-save"></i> Save Matrix</button>
    </div>

    <div class="sidebar-section route-list">
      <button v-for="policy in props.policies" :key="policy.id" class="sidebar-row" :class="{ active: selectedPolicyId === policy.id }" type="button" @click="selectedPolicyId = policy.id">
        <i class="bi bi-signpost-split"></i>
        <span class="sidebar-row-name">{{ policy.name }}</span>
        <span class="route-count">{{ policy.candidates.length }}</span>
      </button>
      <button class="sidebar-add-button" type="button" title="New route" @click="addPolicy"><i class="bi bi-plus-lg"></i></button>
    </div>

    <form v-if="selectedPolicy" class="route-editor" @submit.prevent="save">
      <div class="editor-title"><span>Routing Policy</span><button class="btn btn-sm icon-button" type="button" @click="selectedPolicyId = ''"><i class="bi bi-x"></i></button></div>
      <label class="field"><span>Name</span><input v-model="selectedPolicy.name" class="form-control form-control-sm" /></label>
      <label class="field"><span>Description</span><textarea v-model="selectedPolicy.description" class="form-control form-control-sm" rows="2"></textarea></label>
      <div class="route-options">
        <label class="setting-check"><input v-model="selectedPolicy.enabled" type="checkbox" class="form-check-input" /><span>Enabled</span></label>
        <label class="setting-check"><input v-model="selectedPolicy.auto_failover" type="checkbox" class="form-check-input" /><span>Automatic failover</span></label>
      </div>
      <div class="route-number-grid">
        <label class="field"><span>Max attempts</span><input v-model.number="selectedPolicy.max_attempts" type="number" min="1" max="20" class="form-control form-control-sm" /></label>
        <label class="field"><span>Cooldown seconds</span><input v-model.number="selectedPolicy.cooldown_seconds" type="number" min="0" class="form-control form-control-sm" /></label>
      </div>
      <div class="candidate-title"><span>Execution Targets</span><button class="btn btn-sm icon-button" type="button" title="Add target" @click="addCandidate(selectedPolicy)"><i class="bi bi-plus-lg"></i></button></div>
      <div v-for="(candidate, index) in selectedPolicy.candidates" :key="candidate.id" class="candidate-card">
        <div class="candidate-order">
          <strong>{{ index + 1 }}</strong>
          <button class="icon-button" type="button" title="Move up" @click="moveCandidate(selectedPolicy, index, -1)"><i class="bi bi-arrow-up"></i></button>
          <button class="icon-button" type="button" title="Move down" @click="moveCandidate(selectedPolicy, index, 1)"><i class="bi bi-arrow-down"></i></button>
          <button class="icon-button danger" type="button" title="Remove" @click="selectedPolicy.candidates.splice(index, 1)"><i class="bi bi-trash"></i></button>
        </div>
        <label class="field"><span>Label</span><input v-model="candidate.name" class="form-control form-control-sm" placeholder="Optional" /></label>
        <label class="field"><span>Runtime</span><select v-model="candidate.runtime_id" class="form-select form-select-sm"><option v-for="provider in props.providers" :key="provider.id" :value="provider.id">{{ provider.name }}</option></select></label>
        <label class="field"><span>Account</span><select v-model="candidate.provider_account_id" class="form-select form-select-sm"><option value="">Runtime default credentials</option><option v-for="account in props.accounts" :key="account.id" :value="account.id">{{ account.name }}</option></select></label>
        <label class="field"><span>Runtime Model ID</span><input v-model="candidate.model_id" class="form-control form-control-sm" placeholder="Provider default" /></label>
        <div class="route-options">
          <label class="setting-check"><input :checked="candidateCapability(candidate, 'tools')" type="checkbox" class="form-check-input" @change="setCandidateCapability(candidate, 'tools', ($event.target as HTMLInputElement).checked)" /><span>Tools</span></label>
          <label class="setting-check"><input :checked="candidateCapability(candidate, 'filesystem')" type="checkbox" class="form-check-input" @change="setCandidateCapability(candidate, 'filesystem', ($event.target as HTMLInputElement).checked)" /><span>Filesystem</span></label>
        </div>
        <label class="field"><span>Context window</span><input :value="candidateContextWindow(candidate) || ''" type="number" min="0" step="1000" class="form-control form-control-sm" placeholder="Unknown" @input="setCandidateContextWindow(candidate, $event)" /></label>
        <label class="setting-check"><input v-model="candidate.enabled" type="checkbox" class="form-check-input" /><span>Enabled</span></label>
      </div>
      <div class="editor-actions"><button class="btn btn-sm btn-outline-danger" type="button" @click="deletePolicy(selectedPolicy)">Delete</button><button class="btn btn-sm btn-primary" type="submit">Save</button></div>
    </form>

    <details class="account-editor">
      <summary>Provider Accounts ({{ props.accounts.length }})</summary>
      <div v-for="(account, index) in props.accounts" :key="account.id" class="account-card">
        <label class="field"><span>Name</span><input v-model="account.name" class="form-control form-control-sm" /></label>
        <label class="field"><span>Provider</span><input v-model="account.provider" class="form-control form-control-sm" placeholder="openai / deepseek / kimi" /></label>
        <label class="field"><span>Credential reference</span><input v-model="account.credential_ref" class="form-control form-control-sm" placeholder="hermes-profile:default:deepseek" /></label>
        <label class="setting-check"><input v-model="account.enabled" type="checkbox" class="form-check-input" /><span>Enabled</span></label>
        <button class="btn btn-sm btn-outline-danger" type="button" @click="props.accounts.splice(index, 1)">Remove</button>
      </div>
      <button class="btn btn-sm" type="button" @click="addAccount"><i class="bi bi-plus-lg"></i> Add Account</button>
    </details>
  </div>
</template>

<style scoped>
.routes-panel,.route-editor,.candidate-card,.account-card,.route-toolbar{display:flex;flex-direction:column;gap:8px}.routes-panel{overflow:auto}.route-toolbar,.route-editor,.account-editor{padding:10px}.route-editor,.account-editor{border-top:1px solid var(--color-border)}.route-list{flex:0 0 auto}.route-count{color:var(--color-text-subtle);font-size:10px}.editor-title,.candidate-title,.candidate-order,.route-options{align-items:center;display:flex;gap:8px}.editor-title,.candidate-title{justify-content:space-between}.route-number-grid{display:grid;gap:8px;grid-template-columns:1fr 1fr}.candidate-card,.account-card{background:var(--color-surface-muted);padding:8px}.candidate-order{justify-content:flex-end}.candidate-order strong{margin-right:auto}.icon-button{background:transparent;border:0;color:inherit}.danger{color:var(--color-danger)}.editor-actions{display:grid;gap:8px;grid-template-columns:1fr 1fr}.account-editor summary{cursor:pointer;margin-bottom:8px}
</style>
