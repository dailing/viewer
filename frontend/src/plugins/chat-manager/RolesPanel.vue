<script setup lang="ts">
import { computed, inject, onMounted, reactive, ref, watch } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import type { AgentCatalog, Role, RoutingConfig } from "../chat/types";
import { errorText } from "../chat/types";
import MasterDetail from "./MasterDetail.vue";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("RolesPanel requires PluginPaneHost");
const ctx = injectedCtx;
const roles = ref<Role[]>([]);
const catalogs = ref<AgentCatalog[]>([]);
const routing = ref<RoutingConfig>({ default_routing_policy_id: "", routing_policies: [] });
const error = ref("");
const form = reactive({ id: "", name: "", description: "", prompt: "", cwd: "", routing_policy_id: "", session_policy: "reuse", context_recycle_percent: "", context_recycle_tokens: "" });
const target = reactive({ agent: "", provider: "", model: "" });
const items = computed(() => roles.value.map(({ id, name }) => ({ id, name })));
const selectedAgent = computed(() => catalogs.value.find((item) => item.agent === target.agent));
const providerOptions = computed(() => selectedAgent.value?.providers ?? []);
const modelOptions = computed(() => providerOptions.value.find((item) => item.provider === target.provider)?.models ?? []);

function syncTarget(resetModel = false): void {
  if (!providerOptions.value.some((item) => item.provider === target.provider)) target.provider = providerOptions.value[0]?.provider ?? "";
  if (resetModel && !modelOptions.value.includes(target.model)) target.model = modelOptions.value[0] ?? "";
}
watch(() => target.agent, () => syncTarget(true));
watch(() => target.provider, () => { if (target.model && !modelOptions.value.includes(target.model)) target.model = modelOptions.value[0] ?? ""; });

async function load(): Promise<void> {
  const [loadedRoles, loadedCatalogs, loadedRouting] = await Promise.all([
    ctx.bus.request("chat:_:roles:list", {}) as Promise<Role[]>,
    ctx.bus.request("chat:_:agent-catalog", {}) as Promise<AgentCatalog[]>,
    ctx.bus.request("chat:_:routing:get", {}) as Promise<RoutingConfig>,
  ]);
  roles.value = loadedRoles;
  catalogs.value = loadedCatalogs;
  routing.value = loadedRouting;
  if (!catalogs.value.some((item) => item.agent === target.agent)) target.agent = catalogs.value.find((item) => item.online)?.agent ?? catalogs.value[0]?.agent ?? "";
  syncTarget();
}

function editByID(id: string): void {
  const role = roles.value.find((item) => item.id === id);
  if (role === undefined) return;
  Object.assign(form, role, {
    context_recycle_percent: role.context_recycle_percent?.toString() ?? "",
    context_recycle_tokens: role.context_recycle_tokens?.toString() ?? "",
  });
}

function reset(): void {
  Object.assign(form, { id: "", name: "", description: "", prompt: "", cwd: "", routing_policy_id: "", session_policy: "reuse", context_recycle_percent: "", context_recycle_tokens: "" });
  error.value = "";
}

async function save(): Promise<void> {
  try {
    const payload = {
      ...form,
      context_recycle_percent: form.context_recycle_percent === "" ? null : Number(form.context_recycle_percent),
      context_recycle_tokens: form.context_recycle_tokens === "" ? null : Number(form.context_recycle_tokens),
    };
    await ctx.bus.request(form.id ? "chat:_:roles:patch" : "chat:_:roles:create", payload);
    reset();
    await load();
  } catch (cause) {
    error.value = errorText(cause);
  }
}

async function addTargetToPolicy(): Promise<void> {
  if (!target.agent || !target.provider) return;
  const generatedID = Math.random().toString(36).slice(2, 10);
  let policy = routing.value.routing_policies.find((item) => item.id === form.routing_policy_id);
  if (policy === undefined) {
    policy = { id: `policy-${generatedID}`, name: `${form.name || "Role"} policy`, enabled: true, auto_failover: false, max_attempts: 1, candidates: [] };
    routing.value.routing_policies.push(policy);
    form.routing_policy_id = policy.id;
    if (!routing.value.default_routing_policy_id) routing.value.default_routing_policy_id = policy.id;
  }
  policy.candidates.push({ id: `candidate-${generatedID}`, name: `${target.agent}/${target.provider}`, agent_id: target.agent, provider_id: target.provider, model_id: target.model, enabled: true, parameters: {} });
  policy.auto_failover = policy.candidates.length > 1;
  policy.max_attempts = policy.candidates.length;
  routing.value = await ctx.bus.request("chat:_:routing:put", routing.value) as RoutingConfig;
}

async function remove(): Promise<void> {
  if (!form.id || !confirm("Delete role?")) return;
  try {
    await ctx.bus.request("chat:_:roles:delete", { id: form.id });
    reset();
    await load();
  } catch (cause) {
    error.value = errorText(cause);
  }
}

onMounted(() => void load().catch((cause) => { error.value = errorText(cause); }));
</script>

<template>
  <MasterDetail :items="items" :selected-id="form.id" create-label="＋ 新建 Role" @select="editByID" @create="reset">
    <template #detail>
      <div v-if="error" class="small text-danger mb-2">{{ error }}</div>
      <div class="row g-2">
        <label class="col-md-6 form-label small">Name<input v-model="form.name" class="form-control form-control-sm mt-1"></label>
        <label class="col-md-6 form-label small">Routing policy<select v-model="form.routing_policy_id" class="form-select form-select-sm mt-1"><option value="">No policy</option><option v-for="policy in routing.routing_policies" :key="policy.id" :value="policy.id">{{ policy.name }}</option></select></label>
        <label class="col-12 form-label small">Router description<input v-model="form.description" class="form-control form-control-sm mt-1"></label>
        <label class="col-12 form-label small">Run prompt<textarea v-model="form.prompt" class="form-control form-control-sm mt-1" rows="5"></textarea></label>
        <label class="col-md-6 form-label small">Role cwd<input v-model="form.cwd" class="form-control form-control-sm mt-1"></label>
        <label class="col-md-6 form-label small">Session policy<select v-model="form.session_policy" class="form-select form-select-sm mt-1"><option value="reuse">reuse</option><option value="new_each_run">new_each_run</option></select></label>
        <label class="col-md-6 form-label small">Context recycle %<input v-model="form.context_recycle_percent" type="number" class="form-control form-control-sm mt-1"></label>
        <label class="col-md-6 form-label small">Context recycle tokens<input v-model="form.context_recycle_tokens" type="number" class="form-control form-control-sm mt-1"></label>
      </div>

      <div class="small text-secondary mt-3 mb-1">用 Agent catalog 构造并绑定 policy candidate</div>
      <div class="row g-1">
        <div class="col-md-3"><select v-model="target.agent" class="form-select form-select-sm"><option v-for="item in catalogs" :key="item.agent" :value="item.agent" :disabled="!item.online">{{ item.agent }}{{ item.online ? "" : " (offline)" }}</option></select></div>
        <div class="col-md-3"><select v-model="target.provider" class="form-select form-select-sm"><option v-for="item in providerOptions" :key="item.provider" :value="item.provider">{{ item.provider }}</option></select></div>
        <div class="col-md-3"><select v-model="target.model" class="form-select form-select-sm"><option value="">Default</option><option v-for="model in modelOptions" :key="model" :value="model">{{ model }}</option></select></div>
        <div class="col-md-3"><button class="btn btn-sm btn-outline-secondary w-100" @click="addTargetToPolicy">加入 Policy</button></div>
      </div>

      <div class="d-flex gap-1 mt-3">
        <button class="btn btn-sm btn-primary" @click="save">{{ form.id ? "保存" : "创建" }}</button>
        <button v-if="form.id" class="btn btn-sm btn-outline-danger" @click="remove"><i class="bi bi-trash me-1"></i>删除</button>
        <button class="btn btn-sm btn-outline-secondary" @click="reset">清空</button>
      </div>
    </template>
  </MasterDetail>
</template>
