<script setup lang="ts">
import { computed, inject, onMounted, reactive, ref, watch } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import type { AgentCatalog, Role } from "./types";
import { errorText } from "./types";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("RolesPanel requires PluginPaneHost");
const ctx: PluginCtx = injectedCtx;
const roles = ref<Role[]>([]);
const catalogs = ref<AgentCatalog[]>([]);
const error = ref("");
const form = reactive({ id: "", name: "", description: "", prompt: "", provider: "hermes", catalog_provider: "default", cwd: "", model: "", routing_policy_id: "", session_policy: "reuse", capability_requirements: "{}", context_recycle_percent: "", context_recycle_tokens: "" });
const selectedAgent = computed(() => catalogs.value.find((item) => item.agent === form.provider));
const providerOptions = computed(() => selectedAgent.value?.providers ?? []);
const modelOptions = computed(() => providerOptions.value.find((item) => item.provider === form.catalog_provider)?.models ?? []);

function syncCatalogSelection(resetModel = false) {
  const providers = providerOptions.value;
  if (!providers.some((item) => item.provider === form.catalog_provider)) form.catalog_provider = providers[0]?.provider ?? "";
  if (resetModel && !modelOptions.value.includes(form.model)) form.model = modelOptions.value[0] ?? "";
}
watch(() => form.provider, () => syncCatalogSelection(true));
watch(() => form.catalog_provider, () => { if (form.model && !modelOptions.value.includes(form.model)) form.model = modelOptions.value[0] ?? ""; });

async function load() {
  const [loadedRoles, loadedCatalogs] = await Promise.all([
    ctx.bus.request("chat:_:roles:list", {}) as Promise<Role[]>,
    ctx.bus.request("chat:_:agent-catalog", {}) as Promise<AgentCatalog[]>,
  ]);
  roles.value = loadedRoles;
  catalogs.value = loadedCatalogs;
  syncCatalogSelection();
}
function edit(role: Role) {
  Object.assign(form, role, { model: role.model ?? "", capability_requirements: JSON.stringify(role.capability_requirements, null, 2), context_recycle_percent: role.context_recycle_percent?.toString() ?? "", context_recycle_tokens: role.context_recycle_tokens?.toString() ?? "" });
  form.catalog_provider = catalogs.value.find((item) => item.agent === role.provider)?.providers[0]?.provider ?? "";
}
function reset() {
  Object.assign(form, { id: "", name: "", description: "", prompt: "", provider: catalogs.value.find((item) => item.online)?.agent ?? "hermes", catalog_provider: "", cwd: "", model: "", routing_policy_id: "", session_policy: "reuse", capability_requirements: "{}", context_recycle_percent: "", context_recycle_tokens: "" });
  syncCatalogSelection();
}
async function save() {
  try {
    const { catalog_provider: _catalogProvider, ...fields } = form;
    const payload = { ...fields, capability_requirements: JSON.parse(form.capability_requirements), model: form.model || null, context_recycle_percent: form.context_recycle_percent === "" ? null : Number(form.context_recycle_percent), context_recycle_tokens: form.context_recycle_tokens === "" ? null : Number(form.context_recycle_tokens) };
    await ctx.bus.request(form.id ? "chat:_:roles:patch" : "chat:_:roles:create", payload);
    reset();
    await load();
  } catch (cause) { error.value = errorText(cause); }
}
async function remove(id: string) { if (confirm("Delete role?")) { await ctx.bus.request("chat:_:roles:delete", { id }); await load(); } }
onMounted(() => void load().catch((cause) => error.value = errorText(cause)));
</script>

<template>
  <section class="p-2 h-100 overflow-auto">
    <h6>Roles</h6>
    <div v-if="error" class="small text-danger">{{ error }}</div>
    <div class="row g-1">
      <div class="col-6"><input v-model="form.name" class="form-control form-control-sm" placeholder="Name"></div>
      <div class="col-6"><select v-model="form.provider" class="form-select form-select-sm"><option v-for="item in catalogs" :key="item.agent" :value="item.agent" :disabled="!item.online">{{ item.agent }}{{ item.online ? "" : " (offline)" }}</option></select></div>
      <div class="col-6"><select v-model="form.catalog_provider" class="form-select form-select-sm" :disabled="!selectedAgent?.online"><option v-for="item in providerOptions" :key="item.provider" :value="item.provider">{{ item.provider }}</option></select></div>
      <div class="col-6"><select v-model="form.model" class="form-select form-select-sm" :disabled="!selectedAgent?.online"><option value="">Default model</option><option v-if="form.model && !modelOptions.includes(form.model)" :value="form.model">{{ form.model }}</option><option v-for="model in modelOptions" :key="model" :value="model">{{ model }}</option></select></div>
      <div class="col-12"><input v-model="form.description" class="form-control form-control-sm" placeholder="Router description"></div>
      <div class="col-12"><textarea v-model="form.prompt" class="form-control form-control-sm" rows="3" placeholder="Role prompt" /></div>
      <div class="col-6"><input v-model="form.cwd" class="form-control form-control-sm" placeholder="Role cwd"></div>
      <div class="col-6"><input v-model="form.routing_policy_id" class="form-control form-control-sm" placeholder="Routing policy id"></div>
      <div class="col-6"><select v-model="form.session_policy" class="form-select form-select-sm"><option value="reuse">reuse</option><option value="new_each_run">new_each_run</option></select></div>
      <div class="col-6"><input v-model="form.context_recycle_percent" class="form-control form-control-sm" placeholder="Recycle %"></div>
      <div class="col-6"><input v-model="form.context_recycle_tokens" class="form-control form-control-sm" placeholder="Recycle tokens"></div>
      <div class="col-12"><textarea v-model="form.capability_requirements" class="form-control form-control-sm font-monospace" rows="2" placeholder="Capabilities JSON" /></div>
    </div>
    <div class="d-flex gap-1 my-2"><button class="btn btn-sm btn-primary" @click="save">{{ form.id ? "Save" : "Create" }}</button><button class="btn btn-sm btn-outline-secondary" @click="reset">Clear</button></div>
    <div v-for="role in roles" :key="role.id" class="d-flex align-items-start gap-1 border-top py-1"><button class="btn btn-sm text-start flex-grow-1" @click="edit(role)"><strong>{{ role.name }}</strong><small class="d-block text-secondary">{{ role.description }}</small></button><button class="btn btn-sm text-danger" title="Delete" @click="remove(role.id)"><i class="bi-trash" /></button></div>
  </section>
</template>
