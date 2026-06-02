<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import {
  createSuperRole,
  deleteSuperRole,
  dispatchSuperWorkspace,
  getSuperWorkspace,
  updateSuperRole,
} from "../api/client";
import DirectoryPicker from "./DirectoryPicker.vue";
import VoiceTextarea from "./VoiceTextarea.vue";
import { useAgentsStore } from "../stores/agents";
import type { AgentSessionSnapshot } from "../types/agents";
import type { SuperRole } from "../types/superWorkspace";
import { parseAgentRef } from "../utils/agents";
import { renderMarkdown } from "../utils/markdownRender";
import { namespacedStorageKey } from "../utils/userProfile";

type DispatchRun = {
  id: string;
  message: string;
  role_ids: string[];
  start_counts: Record<string, number>;
  rationale: string;
  created_at: number;
};

const RUNS_KEY = "viewer.superWorkspace.runs.v1";
const agents = useAgentsStore();
const roles = ref<SuperRole[]>([]);
const selectedRoleId = ref("");
const snapshots = ref<Record<string, AgentSessionSnapshot>>({});
const runs = ref<DispatchRun[]>([]);
const composer = ref("");
const error = ref("");
const busy = ref(false);
const roleSaving = ref(false);
const routeAllRoles = ref(true);
const selectedRole = computed(() => roles.value.find((role) => role.id === selectedRoleId.value) ?? null);
const dispatchableRoles = computed(() => roles.value.filter((role) => role.description.trim()));
let pollTimer: number | null = null;

onMounted(async () => {
  loadRuns();
  await agents.loadProviders();
  await load();
  pollTimer = window.setInterval(() => {
    void refreshSnapshots();
  }, 3000);
});

onUnmounted(() => {
  if (pollTimer !== null) window.clearInterval(pollTimer);
});

async function load() {
  const data = await getSuperWorkspace();
  roles.value = data.roles;
  if (!selectedRoleId.value || !roles.value.some((role) => role.id === selectedRoleId.value)) {
    selectedRoleId.value = "";
  }
  await refreshSnapshots();
}

async function refreshSnapshots() {
  const next = { ...snapshots.value };
  await Promise.all(
    roles.value.map(async (role) => {
      if (!role.session_ref || !parseAgentRef(role.session_ref)) return;
      try {
        next[role.session_ref] = await agents.snapshot(role.session_ref, "focus");
      } catch {
        delete next[role.session_ref];
      }
    }),
  );
  snapshots.value = next;
}

async function addRole() {
  roleSaving.value = true;
  error.value = "";
  try {
    const data = await createSuperRole({ name: `Role ${roles.value.length + 1}`, provider: "codex" });
    roles.value = data.roles;
    selectedRoleId.value = roles.value[roles.value.length - 1]?.id ?? "";
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    roleSaving.value = false;
  }
}

async function saveSelectedRole() {
  if (!selectedRole.value) return;
  roleSaving.value = true;
  error.value = "";
  try {
    const role = selectedRole.value;
    const data = await updateSuperRole(role.id, {
      name: role.name,
      description: role.description,
      provider: role.provider || "codex",
      cwd: role.cwd,
      model: role.model ?? null,
      session_ref: role.session_ref,
    });
    roles.value = data.roles;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    roleSaving.value = false;
  }
}

async function removeSelectedRole() {
  if (!selectedRole.value) return;
  roleSaving.value = true;
  error.value = "";
  try {
    const roleId = selectedRole.value.id;
    const data = await deleteSuperRole(roleId);
    roles.value = data.roles;
    selectedRoleId.value = "";
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    roleSaving.value = false;
  }
}

async function renewSelectedRole() {
  if (!selectedRole.value) return;
  await renewRole(selectedRole.value);
}

async function renewRole(role: SuperRole) {
  busy.value = true;
  error.value = "";
  try {
    await createSessionForRole(role);
    await refreshSnapshots();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    busy.value = false;
  }
}

function toggleRoleConfig(roleId: string) {
  selectedRoleId.value = selectedRoleId.value === roleId ? "" : roleId;
}

async function dispatchMessage() {
  const message = composer.value.trim();
  if (!message || busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    const roleIds = routeAllRoles.value ? undefined : selectedRole.value ? [selectedRole.value.id] : undefined;
    const dispatch = await dispatchSuperWorkspace(message, roleIds);
    const selected = roles.value.filter((role) => dispatch.role_ids.includes(role.id));
    const run: DispatchRun = {
      id: crypto.randomUUID(),
      message,
      role_ids: selected.map((role) => role.id),
      start_counts: {},
      rationale: dispatch.rationale,
      created_at: Date.now() / 1000,
    };
    runs.value = [run, ...runs.value].slice(0, 80);
    saveRuns();
    for (const role of selected) {
      const snapshot = role.session_ref ? snapshots.value[role.session_ref] : undefined;
      run.start_counts[role.id] = snapshot?.event_count ?? 0;
      await sendMessageToRole(role, message);
    }
    composer.value = "";
    saveRuns();
    await agents.load();
    await refreshSnapshots();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    busy.value = false;
  }
}

function handleComposerKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" || (!event.metaKey && !event.ctrlKey)) return;
  event.preventDefault();
  void dispatchMessage();
}

async function sendMessageToRole(role: SuperRole, message: string) {
  if (!role.session_ref || !parseAgentRef(role.session_ref)) {
    await createSessionForRole(role, message);
    return;
  }
  const snapshot = snapshots.value[role.session_ref] ?? (await agents.snapshot(role.session_ref, "focus"));
  const prompt = roleMessagePrompt(message);
  if (snapshot.status === "running") {
    await agents.queue(role.session_ref, prompt, role.model ?? undefined);
    return;
  }
  await agents.send(role.session_ref, prompt, role.model ?? undefined);
}

async function createSessionForRole(role: SuperRole, firstMessage = "") {
  const prompt = firstMessage ? `${bootstrapPrompt(role)}\n\n${roleMessagePrompt(firstMessage)}` : bootstrapPrompt(role);
  const session = await agents.create(role.provider || "codex", prompt, role.cwd, role.model ?? undefined);
  const data = await updateSuperRole(role.id, { session_ref: session.ref });
  roles.value = data.roles;
}

function bootstrapPrompt(role: SuperRole) {
  return `You are a persistent Super Workspace role named "${role.name}".

Fixed role rules:
${role.description || "(No role rules were provided.)"}

Operate as this role only. Prefer work that matches the fixed rules, files, topic, and responsibilities above. If a later user message appears unrelated to this role, say so briefly and ask for clarification instead of silently switching tasks.`;
}

function roleMessagePrompt(message: string) {
  return `Super Workspace routed this message to your role. Apply your fixed role rules and answer only for your own responsibility.

User message:
${message}`;
}

function roleSnapshot(role: SuperRole) {
  return role.session_ref ? snapshots.value[role.session_ref] : undefined;
}

function roleStatus(role: SuperRole) {
  return roleSnapshot(role)?.status ?? "idle";
}

function roleDone(role: SuperRole) {
  const status = roleStatus(role);
  return status === "exited" || status === "failed";
}

function contextPercent(role: SuperRole) {
  const value = Number(roleSnapshot(role)?.raw?.context_used_percent);
  if (!Number.isFinite(value)) return "";
  return `${Math.round(value)}%`;
}

function runRoles(run: DispatchRun) {
  return run.role_ids.map((id) => roles.value.find((role) => role.id === id)).filter((role): role is SuperRole => Boolean(role));
}

function runRoleEvents(run: DispatchRun, role: SuperRole) {
  const snapshot = roleSnapshot(role);
  if (!snapshot) return [];
  const start = run.start_counts[role.id] ?? 0;
  return snapshot.events.filter((event) => event.index >= start && event.text.trim());
}

function runRoleHtml(run: DispatchRun, role: SuperRole) {
  const text = runRoleEvents(run, role)
    .map((event) => event.text)
    .filter(Boolean)
    .join("\n\n");
  return text ? renderMarkdown(text, { baseDirectory: roleSnapshot(role)?.cwd_relative ?? "" }) : "";
}

function roleProviderName(role: SuperRole) {
  return agents.providerById(role.provider || "codex").name;
}

function roleIcon(role: SuperRole) {
  return agents.providerById(role.provider || "codex").icon;
}

function formatTime(value: number) {
  return new Date(value * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function loadRuns() {
  try {
    const parsed = JSON.parse(localStorage.getItem(namespacedStorageKey(RUNS_KEY)) || "[]");
    runs.value = Array.isArray(parsed) ? parsed.slice(0, 80) : [];
  } catch {
    runs.value = [];
  }
}

function saveRuns() {
  localStorage.setItem(namespacedStorageKey(RUNS_KEY), JSON.stringify(runs.value.slice(0, 80)));
}
</script>

<template>
  <div class="super-page">
    <aside class="super-roles">
      <div class="super-roles-header">
        <div class="super-section-title">Roles</div>
        <button class="btn btn-sm btn-primary icon-button" type="button" title="Add role" :disabled="roleSaving" @click="addRole">
          <i class="bi bi-plus-lg"></i>
        </button>
      </div>
      <div
        v-for="role in roles"
        :key="role.id"
        class="super-role-row"
        :class="{ active: role.id === selectedRoleId }"
        role="button"
        tabindex="0"
        @click="toggleRoleConfig(role.id)"
        @keydown.enter.prevent="toggleRoleConfig(role.id)"
        @keydown.space.prevent="toggleRoleConfig(role.id)"
      >
        <i class="bi" :class="roleIcon(role)"></i>
        <span class="super-role-name">{{ role.name }}</span>
        <span class="super-role-status" :class="`status-${roleStatus(role)}`"></span>
        <button
          class="btn btn-sm btn-outline-primary super-role-renew"
          type="button"
          title="Renew role session"
          :disabled="busy"
          @click.stop="renewRole(role)"
        >
          <i class="bi bi-plus-circle"></i>
          <span>Renew</span>
        </button>
      </div>
      <div v-if="!roles.length" class="super-empty">No roles yet</div>

      <section v-if="selectedRole" class="super-role-config">
        <div class="super-section-title">Config</div>
        <label class="super-field">
          <span>Name</span>
          <input v-model="selectedRole.name" class="form-control form-control-sm" />
        </label>
        <label class="super-field">
          <span>Provider</span>
          <select v-model="selectedRole.provider" class="form-select form-select-sm">
            <option v-for="provider in agents.providers" :key="provider.id" :value="provider.id">{{ provider.name }}</option>
          </select>
        </label>
        <label class="super-field">
          <span>CWD</span>
          <DirectoryPicker v-model="selectedRole.cwd" />
        </label>
        <label class="super-field">
          <span>Model</span>
          <input v-model="selectedRole.model" class="form-control form-control-sm" placeholder="Default" />
        </label>
        <label class="super-field">
          <span>Rules</span>
          <VoiceTextarea
            v-model="selectedRole.description"
            :context-id="`super-role:${selectedRole.id}:rules`"
            placeholder="Fixed role rules, file scope, responsibilities, and constraints."
            min-height="180px"
            :rows="8"
          />
        </label>
        <div class="super-role-meta">
          <span>{{ roleProviderName(selectedRole) }}</span>
          <span>{{ roleStatus(selectedRole) }}</span>
          <span v-if="contextPercent(selectedRole)">ctx {{ contextPercent(selectedRole) }}</span>
        </div>
        <div class="super-config-actions">
          <button class="btn btn-sm btn-outline-secondary" type="button" :disabled="roleSaving" @click="saveSelectedRole">Save</button>
          <button class="btn btn-sm btn-outline-primary" type="button" :disabled="busy" title="Renew session" @click="renewSelectedRole">
            <i class="bi bi-plus-circle"></i>
            <span>Renew</span>
          </button>
          <button class="btn btn-sm btn-outline-danger" type="button" :disabled="roleSaving" @click="removeSelectedRole">Delete</button>
        </div>
      </section>
    </aside>

    <main class="super-chat">
      <div class="super-composer">
        <VoiceTextarea
          v-model="composer"
          context-id="super-workspace:composer"
          placeholder="Write one message; the dispatcher will choose the role session."
          :rows="3"
          @keydown="handleComposerKeydown"
        >
          <template #actions>
            <label class="super-check">
              <input v-model="routeAllRoles" type="checkbox" />
              <span>Route across all roles</span>
            </label>
            <button class="btn btn-primary" type="button" :disabled="busy || !composer.trim() || !dispatchableRoles.length" @click="dispatchMessage">
              <i class="bi bi-send"></i>
              <span>{{ busy ? "Dispatching" : "Dispatch" }}</span>
            </button>
          </template>
        </VoiceTextarea>
        <div v-if="error" class="super-error">{{ error }}</div>
      </div>

      <section class="super-thread">
        <article v-for="run in runs" :key="run.id" class="super-run">
          <div class="super-user-message">
            <div class="super-run-time">{{ formatTime(run.created_at) }}</div>
            <div class="super-message-text">{{ run.message }}</div>
          </div>
          <div class="super-route-line">
            <span v-for="role in runRoles(run)" :key="role.id" class="super-route-chip">
              <i class="bi" :class="roleIcon(role)"></i>
              {{ role.name }}
            </span>
            <span v-if="run.rationale" class="super-rationale">{{ run.rationale }}</span>
          </div>
          <div v-for="role in runRoles(run)" :key="`${run.id}:${role.id}`" class="super-role-response">
            <div class="super-response-head">
              <span class="super-response-role">{{ role.name }}</span>
              <span class="super-response-status">{{ roleStatus(role) }}</span>
            </div>
            <div v-if="roleDone(role) && runRoleHtml(run, role)" class="markdown-body super-response-body" v-html="runRoleHtml(run, role)"></div>
            <div v-else class="super-waiting">Waiting for response</div>
          </div>
        </article>
        <div v-if="!runs.length" class="super-empty-thread">Create roles, write one message, and dispatch it into the persistent role sessions.</div>
      </section>
    </main>
  </div>
</template>

<style scoped>
.super-page {
  background: #f6f7f9;
  color: var(--text);
  display: grid;
  grid-template-columns: 320px minmax(0, 1fr);
  height: 100%;
  min-height: 0;
}

.super-roles {
  background: #ffffff;
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  min-height: 0;
  overflow: auto;
  padding: 10px;
}

.super-roles-header,
.super-config-actions,
.super-composer-actions,
.super-response-head,
.super-route-line,
.super-role-meta {
  align-items: center;
  display: flex;
  gap: 8px;
}

.super-roles-header {
  justify-content: space-between;
  margin-bottom: 8px;
}

.super-section-title {
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0;
  text-transform: uppercase;
}

.super-role-row {
  align-items: center;
  background: transparent;
  border: 1px solid transparent;
  border-radius: 6px;
  color: inherit;
  cursor: pointer;
  display: grid;
  gap: 8px;
  grid-template-columns: auto minmax(0, 1fr) auto auto;
  min-height: 34px;
  padding: 6px 8px;
  text-align: left;
}

.super-role-row.active,
.super-role-row:hover {
  background: #eef3ff;
  border-color: #c8d8ff;
}

.super-role-row:focus-visible {
  outline: 2px solid #86b7fe;
  outline-offset: 2px;
}

.super-role-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.super-role-status {
  border-radius: 999px;
  height: 8px;
  width: 8px;
}

.super-role-renew {
  align-items: center;
  display: inline-flex;
  gap: 5px;
  min-width: 76px;
  justify-content: center;
}

.status-idle,
.status-exited {
  background: #8ca0bd;
}

.status-running {
  background: #0d6efd;
}

.status-failed {
  background: #dc3545;
}

.super-role-config {
  border-top: 1px solid var(--border);
  margin-top: 12px;
  padding-top: 12px;
}

.super-field {
  display: grid;
  gap: 4px;
  margin-top: 9px;
}

.super-field span {
  color: var(--text-muted);
  font-size: 12px;
}

.super-role-meta {
  color: var(--text-muted);
  font-size: 12px;
  justify-content: space-between;
  margin: 10px 0;
}

.super-config-actions {
  flex-wrap: wrap;
}

.super-chat {
  display: grid;
  grid-template-rows: auto minmax(0, 1fr);
  min-height: 0;
}

.super-composer {
  background: #ffffff;
  border-bottom: 1px solid var(--border);
  display: grid;
  gap: 8px;
  padding: 12px;
}

.super-check {
  align-items: center;
  color: var(--text-muted);
  display: inline-flex;
  font-size: 13px;
  gap: 7px;
}

.super-thread {
  min-height: 0;
  overflow: auto;
  padding: 16px;
}

.super-run {
  display: grid;
  gap: 8px;
  margin: 0 auto 18px;
  max-width: 980px;
}

.super-user-message,
.super-role-response {
  background: #ffffff;
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px 12px;
}

.super-user-message {
  margin-left: auto;
  max-width: 760px;
}

.super-run-time,
.super-rationale,
.super-response-status,
.super-waiting,
.super-empty,
.super-empty-thread {
  color: var(--text-muted);
  font-size: 12px;
}

.super-message-text {
  white-space: pre-wrap;
}

.super-route-line {
  flex-wrap: wrap;
  padding-left: 4px;
}

.super-route-chip {
  align-items: center;
  background: #e9eef8;
  border: 1px solid #ccd8ed;
  border-radius: 999px;
  display: inline-flex;
  font-size: 12px;
  gap: 5px;
  padding: 3px 8px;
}

.super-response-head {
  border-bottom: 1px solid var(--border);
  justify-content: space-between;
  margin-bottom: 8px;
  padding-bottom: 6px;
}

.super-response-role {
  font-weight: 700;
}

.super-response-body {
  font-size: var(--markdown-body-font-size);
}

.super-error {
  color: #a33;
  font-size: 13px;
}

.super-empty,
.super-empty-thread {
  padding: 10px 4px;
}

@media (max-width: 760px) {
  .super-page {
    grid-template-columns: 1fr;
    grid-template-rows: auto minmax(0, 1fr);
  }

  .super-roles {
    border-bottom: 1px solid var(--border);
    border-right: 0;
    max-height: 44vh;
  }
}
</style>
