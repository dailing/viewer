<script setup lang="ts">
import { computed, ref } from "vue";
import type { RoutingPolicyConfig } from "../../types/files";
import type { SuperRole } from "../../types/superWorkspace";
import DirectoryPicker from "../DirectoryPicker.vue";

const props = defineProps<{
  roles: SuperRole[];
  routingPolicies: RoutingPolicyConfig[];
}>();

const emit = defineEmits<{
  "create-role": [];
  "update-role": [role: SuperRole];
  "delete-role": [role: SuperRole];
}>();

const selectedRoleId = ref("");
const selectedRole = computed(() => props.roles.find((role) => role.id === selectedRoleId.value) ?? null);
const selectedPolicy = computed(() => props.routingPolicies.find((policy) => policy.id === selectedRole.value?.routing_policy_id));

function selectRole(role: SuperRole) {
  selectedRoleId.value = selectedRoleId.value === role.id ? "" : role.id;
}

</script>

<template>
  <div class="sidebar-panel">
    <div class="sidebar-section list-section" :class="{ editing: selectedRole }">
      <div v-if="!props.roles.length" class="empty-panel">No roles</div>
      <button
        v-for="role in props.roles"
        :key="role.id"
        class="sidebar-row"
        :class="{ active: role.id === selectedRoleId }"
        type="button"
        :title="role.name"
        @click="selectRole(role)"
      >
        <i class="bi bi-person-gear"></i>
        <span class="sidebar-row-name">{{ role.name }}</span>
      </button>
    </div>

    <div class="sidebar-action-footer">
      <button class="sidebar-add-button" type="button" title="New role" aria-label="New role" @click="emit('create-role')">
        <i class="bi bi-plus-lg"></i>
      </button>
    </div>

    <form v-if="selectedRole" class="role-editor" @submit.prevent="emit('update-role', selectedRole)">
      <div class="editor-title">
        <span>Edit Role</span>
        <button class="btn btn-sm icon-button" type="button" title="Close" @click="selectedRoleId = ''">
          <i class="bi bi-x"></i>
        </button>
      </div>
      <label class="field">
        <span>Name</span>
        <input v-model="selectedRole.name" class="form-control form-control-sm" />
      </label>
      <label class="field">
        <span>Routing Policy</span>
        <select v-model="selectedRole.routing_policy_id" class="form-select form-select-sm">
          <option v-for="policy in props.routingPolicies" :key="policy.id" :value="policy.id">{{ policy.name }}</option>
        </select>
        <small>{{ selectedPolicy?.description || 'Runtime, account, and model are resolved when each turn starts.' }}</small>
      </label>
      <label class="field">
        <span>Description</span>
        <small>For dispatch: when to call this role, its capabilities, scope, and routing constraints.</small>
        <textarea v-model="selectedRole.description" class="form-control form-control-sm" rows="4"></textarea>
      </label>
      <label class="field">
        <span>Role Prompt</span>
        <small>For the Agent: operating rules, workflow, standards, and style. Changing this starts a fresh session next time.</small>
        <textarea v-model="selectedRole.prompt" class="form-control form-control-sm" rows="7"></textarea>
      </label>
      <label class="field">
        <span>Working Directory</span>
        <DirectoryPicker v-model="selectedRole.cwd" empty-label="Chat root" clear-title="Use the chat root" />
      </label>
      <div class="field">
        <span>Capability Requirements</span>
        <label class="setting-check"><input v-model="selectedRole.capability_requirements.tools" type="checkbox" class="form-check-input" /><span>Tool calls</span></label>
        <label class="setting-check"><input v-model="selectedRole.capability_requirements.filesystem" type="checkbox" class="form-check-input" /><span>Filesystem access</span></label>
        <label class="field compact-field"><span>Minimum context tokens</span><input v-model.number="selectedRole.capability_requirements.min_context_window" type="number" min="0" step="1000" class="form-control form-control-sm" /></label>
      </div>
      <label class="field">
        <span>Session Management</span>
        <select v-model="selectedRole.session_policy" class="form-select form-select-sm">
          <option value="reuse">Reuse session</option>
          <option value="new_each_run">New session each run</option>
        </select>
      </label>
      <label class="field">
        <span>Context Recycle % (optional)</span>
        <input
          v-model.number="selectedRole.context_recycle_percent"
          type="number"
          min="1"
          max="100"
          step="1"
          class="form-control form-control-sm"
          placeholder="e.g. 80"
        />
      </label>
      <label class="field">
        <span>Context Recycle Tokens (optional)</span>
        <input
          v-model.number="selectedRole.context_recycle_tokens"
          type="number"
          min="1000"
          step="1000"
          class="form-control form-control-sm"
          placeholder="e.g. 200000"
        />
      </label>
      <div class="editor-actions">
        <button class="btn btn-sm btn-primary" type="submit">
          <i class="bi bi-save"></i>
          <span>Save</span>
        </button>
        <button class="btn btn-sm btn-outline-danger" type="button" @click="emit('delete-role', selectedRole)">
          <i class="bi bi-trash"></i>
          <span>Delete</span>
        </button>
      </div>
    </form>
  </div>
</template>

<style scoped>
.list-section.editing {
  flex: 0 1 34%;
}

.panel-command,
.editor-actions .btn {
  align-items: center;
  display: inline-flex;
  gap: 7px;
  justify-content: center;
}

.role-editor {
  border-top: 1px solid var(--color-border);
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 9px;
  min-height: 0;
  overflow: auto;
  padding: 10px;
}

.field small {
  color: var(--color-text-subtle);
  font-size: 10px;
  line-height: 1.35;
}

.editor-title {
  align-items: center;
  color: var(--color-text);
  display: flex;
  font-size: 12px;
  font-weight: 700;
  justify-content: space-between;
}

.editor-actions {
  display: grid;
  gap: 8px;
  grid-template-columns: 1fr 1fr;
}
</style>
