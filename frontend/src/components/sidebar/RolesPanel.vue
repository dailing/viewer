<script setup lang="ts">
import { computed, ref } from "vue";
import type { AgentProvider } from "../../types/agents";
import type { SuperRole } from "../../types/superWorkspace";
import DirectoryPicker from "../DirectoryPicker.vue";

const props = defineProps<{
  roles: SuperRole[];
  providers: { id: AgentProvider; name: string }[];
}>();

const emit = defineEmits<{
  "create-role": [];
  "update-role": [role: SuperRole];
  "delete-role": [role: SuperRole];
}>();

const selectedRoleId = ref("");
const selectedRole = computed(() => props.roles.find((role) => role.id === selectedRoleId.value) ?? null);

function selectRole(role: SuperRole) {
  selectedRoleId.value = selectedRoleId.value === role.id ? "" : role.id;
}
</script>

<template>
  <div class="sidebar-panel">
    <div class="sidebar-section">
      <button class="btn btn-sm btn-primary panel-command" type="button" @click="emit('create-role')">
        <i class="bi bi-person-plus"></i>
        <span>New Role</span>
      </button>
    </div>

    <div class="sidebar-section list-section" :class="{ editing: selectedRole }">
      <div class="section-title">Roles</div>
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
        <i class="bi" :class="role.provider === 'hermes' ? 'bi-lightning' : 'bi-stars'"></i>
        <span class="sidebar-row-name">{{ role.name }}</span>
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
        <span>Provider</span>
        <select v-model="selectedRole.provider" class="form-select form-select-sm">
          <option v-for="provider in props.providers" :key="provider.id" :value="provider.id">{{ provider.name }}</option>
        </select>
      </label>
      <label class="field">
        <span>Description / Rules</span>
        <textarea v-model="selectedRole.description" class="form-control form-control-sm" rows="5"></textarea>
      </label>
      <label class="field">
        <span>Working Directory</span>
        <DirectoryPicker v-model="selectedRole.cwd" empty-label="Inherit chat cwd" clear-title="Leave role cwd empty" />
      </label>
      <label class="field">
        <span>Model</span>
        <input v-model="selectedRole.model" class="form-control form-control-sm" placeholder="Provider default" />
      </label>
      <label class="field">
        <span>Session Management</span>
        <select v-model="selectedRole.session_policy" class="form-select form-select-sm">
          <option value="reuse">Reuse session</option>
          <option value="new_each_run">New session each run</option>
        </select>
      </label>
      <label class="field">
        <span>Session Ref</span>
        <input v-model="selectedRole.session_ref" class="form-control form-control-sm" placeholder="provider:id" />
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
.sidebar-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.sidebar-section {
  border-bottom: 1px solid var(--border);
  padding: 10px;
}

.list-section {
  border-bottom: 0;
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
}

.list-section.editing {
  flex: 0 1 34%;
}

.section-title {
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0;
  margin-bottom: 6px;
  text-transform: uppercase;
}

.panel-command,
.editor-actions .btn {
  align-items: center;
  display: inline-flex;
  gap: 7px;
  justify-content: center;
}

.panel-command {
  width: 100%;
}

.empty-panel {
  color: var(--text-muted);
  font-size: 12px;
  padding: 4px 6px;
}

.sidebar-row {
  align-items: center;
  background: transparent;
  border: 1px solid transparent;
  border-radius: 6px;
  color: inherit;
  display: flex;
  gap: 7px;
  min-height: 30px;
  padding: 3px 6px;
  text-align: left;
  width: 100%;
}

.sidebar-row:hover {
  background: #eef3f8;
}

.sidebar-row.active {
  border-color: #2f6fdd;
  box-shadow: inset 0 0 0 1px rgb(47 111 221 / 0.18);
}

.sidebar-row-name {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.role-editor {
  border-top: 1px solid var(--border);
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 9px;
  min-height: 0;
  overflow: auto;
  padding: 10px;
}

.editor-title {
  align-items: center;
  color: #1f2937;
  display: flex;
  font-size: 12px;
  font-weight: 700;
  justify-content: space-between;
}

.field {
  display: grid;
  gap: 4px;
}

.field span {
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 700;
}

.editor-actions {
  display: grid;
  gap: 8px;
  grid-template-columns: 1fr 1fr;
}
</style>
