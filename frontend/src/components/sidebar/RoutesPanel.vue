<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { RoutingPolicyConfig } from "../../types/files";
import type { InferenceCatalog, InferenceTarget, RoutingConfigData } from "../../types/superWorkspace";

const props = defineProps<{
  policies: RoutingPolicyConfig[];
  defaultPolicyId: string;
  catalog: InferenceCatalog;
}>();
const emit = defineEmits<{
  save: [config: RoutingConfigData];
  refresh: [];
}>();

const selectedPolicyId = ref("");
const selectedTargetId = ref("");
const defaultPolicyId = ref(props.defaultPolicyId);
watch(() => props.defaultPolicyId, (value) => { defaultPolicyId.value = value; });
const selectedPolicy = computed(() => props.policies.find((policy) => policy.id === selectedPolicyId.value) ?? null);
const groupedTargets = computed(() => {
  const groups = new Map<string, { label: string; targets: InferenceTarget[] }>();
  for (const target of props.catalog.targets) {
    const key = `${target.agent_id}/${target.provider_id}`;
    const group = groups.get(key) ?? { label: `${target.agent_name} · ${target.provider_name}`, targets: [] };
    group.targets.push(target);
    groups.set(key, group);
  }
  return [...groups.entries()].map(([key, value]) => ({ key, ...value }));
});

function uid(prefix: string) {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`;
}

function addProfile() {
  const id = uid("profile");
  props.policies.push({ id, name: "New Profile", description: "", enabled: true, auto_failover: true, max_attempts: 3, cooldown_seconds: 600, candidates: [] });
  selectedPolicyId.value = id;
  if (!defaultPolicyId.value) defaultPolicyId.value = id;
}

function deleteProfile(policy: RoutingPolicyConfig) {
  if (!window.confirm(`Delete profile "${policy.name}"?`)) return;
  const index = props.policies.findIndex((item) => item.id === policy.id);
  if (index >= 0) props.policies.splice(index, 1);
  if (defaultPolicyId.value === policy.id) defaultPolicyId.value = props.policies[0]?.id ?? "";
  selectedPolicyId.value = "";
}

function addTarget(policy: RoutingPolicyConfig) {
  const target = props.catalog.targets.find((item) => item.target_id === selectedTargetId.value);
  if (!target) return;
  policy.candidates.push({
    id: uid("candidate"), name: "", target_id: target.target_id,
    agent_id: target.agent_id, provider_id: target.provider_id, model_id: target.model_id,
    selection_id: target.selection_id, enabled: true,
    parameters: { capabilities: target.capabilities, context_window: target.context_window },
  });
}

function move(policy: RoutingPolicyConfig, index: number, delta: number) {
  const destination = index + delta;
  if (destination < 0 || destination >= policy.candidates.length) return;
  const [candidate] = policy.candidates.splice(index, 1);
  policy.candidates.splice(destination, 0, candidate);
}

function targetLabel(targetId: string) {
  const target = props.catalog.targets.find((item) => item.target_id === targetId);
  if (target) return `${target.agent_name} · ${target.provider_name} · ${target.model_name}`;
  const candidate = props.policies.flatMap((policy) => policy.candidates).find((item) => item.target_id === targetId);
  if (candidate && !candidate.selection_id) return `${candidate.agent_id} · Agent configured default`;
  return "Unavailable or stale target";
}

function targetAvailable(targetId: string) {
  const target = props.catalog.targets.find((item) => item.target_id === targetId);
  if (target) return target.available;
  return props.policies.some((policy) => policy.candidates.some((item) => item.target_id === targetId && !item.selection_id));
}

function save() {
  emit("save", { default_routing_policy_id: defaultPolicyId.value, routing_policies: props.policies });
}
</script>

<template>
  <div class="sidebar-panel routes-panel">
    <div class="route-toolbar">
      <label class="field"><span>Workspace Default</span><select v-model="defaultPolicyId" class="form-select form-select-sm"><option v-for="policy in props.policies" :key="policy.id" :value="policy.id">{{ policy.name }}</option></select></label>
      <div class="toolbar-actions"><button class="btn btn-sm" type="button" title="Refresh targets" @click="emit('refresh')"><i class="bi bi-arrow-clockwise"></i></button><button class="btn btn-sm btn-primary" type="button" @click="save"><i class="bi bi-save"></i> Save Profiles</button></div>
      <div v-for="warning in props.catalog.warnings" :key="warning" class="catalog-warning">{{ warning }}</div>
    </div>

    <div class="sidebar-section route-list">
      <button v-for="policy in props.policies" :key="policy.id" class="sidebar-row" :class="{ active: selectedPolicyId === policy.id }" type="button" @click="selectedPolicyId = policy.id"><i class="bi bi-signpost-split"></i><span class="sidebar-row-name">{{ policy.name }}</span><span class="route-count">{{ policy.candidates.length }}</span></button>
      <button class="sidebar-add-button" type="button" title="New profile" @click="addProfile"><i class="bi bi-plus-lg"></i></button>
    </div>

    <form v-if="selectedPolicy" class="route-editor" @submit.prevent="save">
      <div class="editor-title"><span>Routing Profile</span><button class="btn btn-sm icon-button" type="button" @click="selectedPolicyId = ''"><i class="bi bi-x"></i></button></div>
      <label class="field"><span>Name</span><input v-model="selectedPolicy.name" class="form-control form-control-sm" /></label>
      <label class="field"><span>Description</span><textarea v-model="selectedPolicy.description" class="form-control form-control-sm" rows="2"></textarea></label>
      <div class="route-options"><label class="setting-check"><input v-model="selectedPolicy.enabled" type="checkbox" class="form-check-input" /><span>Enabled</span></label><label class="setting-check"><input v-model="selectedPolicy.auto_failover" type="checkbox" class="form-check-input" /><span>Automatic failover</span></label></div>
      <div class="route-number-grid"><label class="field"><span>Max attempts</span><input v-model.number="selectedPolicy.max_attempts" type="number" min="1" max="20" class="form-control form-control-sm" /></label><label class="field"><span>Cooldown seconds</span><input v-model.number="selectedPolicy.cooldown_seconds" type="number" min="0" class="form-control form-control-sm" /></label></div>
      <div class="candidate-title"><span>Ordered Targets</span></div>
      <div class="target-picker"><select v-model="selectedTargetId" class="form-select form-select-sm"><option value="">Select agent · provider · model</option><optgroup v-for="group in groupedTargets" :key="group.key" :label="group.label"><option v-for="target in group.targets" :key="target.target_id" :value="target.target_id" :disabled="!target.available">{{ target.model_name }}{{ target.available ? '' : ' (cooldown)' }}</option></optgroup></select><button class="btn btn-sm" type="button" :disabled="!selectedTargetId" @click="addTarget(selectedPolicy)"><i class="bi bi-plus-lg"></i></button></div>
      <div v-for="(candidate, index) in selectedPolicy.candidates" :key="candidate.id" class="candidate-card" :class="{ unavailable: !targetAvailable(candidate.target_id) }">
        <div class="candidate-order"><strong>{{ index + 1 }}</strong><button class="icon-button" type="button" title="Move up" @click="move(selectedPolicy, index, -1)"><i class="bi bi-arrow-up"></i></button><button class="icon-button" type="button" title="Move down" @click="move(selectedPolicy, index, 1)"><i class="bi bi-arrow-down"></i></button><button class="icon-button danger" type="button" title="Remove" @click="selectedPolicy.candidates.splice(index, 1)"><i class="bi bi-trash"></i></button></div>
        <div class="target-name">{{ targetLabel(candidate.target_id) }}</div>
        <div class="target-id">{{ candidate.agent_id }} / {{ candidate.provider_id }} / {{ candidate.model_id }}</div>
        <label class="field"><span>Label</span><input v-model="candidate.name" class="form-control form-control-sm" placeholder="Optional" /></label>
        <label class="setting-check"><input v-model="candidate.enabled" type="checkbox" class="form-check-input" /><span>Enabled</span></label>
      </div>
      <div class="editor-actions"><button class="btn btn-sm btn-outline-danger" type="button" @click="deleteProfile(selectedPolicy)">Delete</button><button class="btn btn-sm btn-primary" type="submit">Save</button></div>
    </form>
  </div>
</template>

<style scoped>
.routes-panel,.route-editor,.candidate-card,.route-toolbar{display:flex;flex-direction:column;gap:8px}.routes-panel{overflow:auto}.route-toolbar,.route-editor{padding:10px}.route-editor{border-top:1px solid var(--color-border)}.route-list{flex:0 0 auto}.route-count,.target-id{color:var(--color-text-subtle);font-size:10px}.editor-title,.candidate-title,.candidate-order,.route-options,.toolbar-actions,.target-picker{align-items:center;display:flex;gap:8px}.editor-title,.candidate-title,.toolbar-actions{justify-content:space-between}.target-picker select{min-width:0}.route-number-grid{display:grid;gap:8px;grid-template-columns:1fr 1fr}.candidate-card{background:var(--color-surface-muted);padding:8px}.candidate-card.unavailable{border-left:3px solid var(--color-danger)}.candidate-order{justify-content:flex-end}.candidate-order strong{margin-right:auto}.target-name{font-weight:600}.icon-button{background:transparent;border:0;color:inherit}.danger,.catalog-warning{color:var(--color-danger)}.catalog-warning{font-size:11px}.editor-actions{display:grid;gap:8px;grid-template-columns:1fr 1fr}
</style>
