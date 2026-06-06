<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from "vue";
import {
  createSuperRole,
  createSuperWorkspaceRun,
  deleteSuperRole,
  getSuperWorkspace,
  listSuperWorkspaceRuns,
  updateSuperWorkspace,
  updateSuperRole,
} from "../api/client";
import { connectSuperWorkspaceEvents } from "../api/events";
import DirectoryPicker from "./DirectoryPicker.vue";
import VoiceTextarea from "./VoiceTextarea.vue";
import { useAgentsStore } from "../stores/agents";
import type { AgentSessionSnapshot } from "../types/agents";
import type { SuperHistoryRun, SuperHistoryTarget, SuperRole } from "../types/superWorkspace";
import { parseAgentRef } from "../utils/agents";
import { renderMarkdown } from "../utils/markdownRender";

const agents = useAgentsStore();
const roles = ref<SuperRole[]>([]);
const selectedRoleId = ref("");
const snapshots = ref<Record<string, AgentSessionSnapshot>>({});
const runs = ref<SuperHistoryRun[]>([]);
const threadRef = ref<HTMLElement | null>(null);
const composer = ref("");
const commonPrompt = ref("");
const error = ref("");
const busy = ref(false);
const roleSaving = ref(false);
const commonPromptSaving = ref(false);
const historyLoading = ref(false);
const loadingOlder = ref(false);
const hasOlderRuns = ref(false);
const nextBefore = ref<number | null>(null);
const runsAfterCursor = ref(0);
const rolePanelOpen = ref(false);
const selectedRole = computed(() => roles.value.find((role) => role.id === selectedRoleId.value) ?? null);
const dispatchableRoles = computed(() => roles.value.filter((role) => role.description.trim()));
const mentionedRoleIds = computed(() => parseLeadingMentionRoleIds(composer.value));
const displayRuns = computed(() => [...runs.value].reverse());
const canDispatch = computed(() => {
  if (!composer.value.trim() || busy.value) return false;
  return true;
});
const dispatchButtonLabel = computed(() => {
  if (busy.value) return "Dispatching";
  if (mentionedRoleIds.value.length) return `Dispatch to ${mentionedRoleIds.value.length}`;
  return "Auto dispatch";
});
let fallbackTimer: number | null = null;
let refreshTimer: number | null = null;
let superWorkspaceEvents: EventSource | null = null;

onMounted(async () => {
  await agents.loadProviders();
  await load();
  await loadRuns(true);
  superWorkspaceEvents = connectSuperWorkspaceEvents(() => {
    scheduleRefreshLiveState(100);
  });
  fallbackTimer = window.setInterval(() => {
    scheduleRefreshLiveState(0);
  }, 30000);
});

onUnmounted(() => {
  if (fallbackTimer !== null) window.clearInterval(fallbackTimer);
  if (refreshTimer !== null) window.clearTimeout(refreshTimer);
  superWorkspaceEvents?.close();
});

async function load() {
  const data = await getSuperWorkspace();
  commonPrompt.value = data.common_prompt ?? "";
  roles.value = data.roles;
  if (!selectedRoleId.value || !roles.value.some((role) => role.id === selectedRoleId.value)) {
    selectedRoleId.value = "";
    rolePanelOpen.value = false;
  }
  await refreshSnapshots();
}

async function refreshLiveState() {
  await refreshSnapshots();
  await loadChangedRuns();
}

function scheduleRefreshLiveState(delayMs: number) {
  if (refreshTimer !== null) return;
  refreshTimer = window.setTimeout(() => {
    refreshTimer = null;
    void refreshLiveState();
  }, delayMs);
}

async function saveCommonPrompt() {
  commonPromptSaving.value = true;
  error.value = "";
  try {
    const data = await updateSuperWorkspace({ common_prompt: commonPrompt.value });
    commonPrompt.value = data.common_prompt ?? "";
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    commonPromptSaving.value = false;
  }
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
    rolePanelOpen.value = Boolean(selectedRoleId.value);
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
    rolePanelOpen.value = false;
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

function handleRoleClick(roleId: string) {
  const role = roles.value.find((item) => item.id === roleId);
  if (!role) return;
  if (selectedRoleId.value === roleId) {
    addRoleMention(role);
    rolePanelOpen.value = true;
    return;
  }
  selectedRoleId.value = roleId;
  rolePanelOpen.value = false;
  addRoleMention(role);
}

function closeRolePanel() {
  rolePanelOpen.value = false;
}

async function dispatchMessage() {
  const message = composer.value.trim();
  if (!message || busy.value) return;
  busy.value = true;
  error.value = "";
  composer.value = "";
  try {
    const run = await createSuperWorkspaceRun({ message });
    upsertRun(run);
    updateRunsAfterCursor([run]);
    await scrollThreadToBottom();
    if (run.status === "failed") {
      error.value = run.error || "Dispatch failed";
      return;
    }
    await agents.load();
    await refreshLiveState();
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

function addRoleMention(role: SuperRole) {
  if (mentionedRoleIds.value.includes(role.id)) return;
  const insert = `@${roleMentionKey(role)} `;
  const position = leadingMentionPrefixEnd(composer.value);
  composer.value = `${composer.value.slice(0, position)}${insert}${composer.value.slice(position)}`;
}

function parseLeadingMentionRoleIds(value: string) {
  const byKey = new Map(roles.value.map((role) => [roleMentionKey(role), role]));
  const ids: string[] = [];
  let position = 0;
  while (position < value.length && value[position] === "@") {
    const match = /^@([A-Za-z_][A-Za-z0-9_]*) /.exec(value.slice(position));
    if (!match) break;
    const role = byKey.get(match[1]);
    if (role && !ids.includes(role.id)) ids.push(role.id);
    position += match[0].length;
  }
  return ids;
}

function leadingMentionPrefixEnd(value: string) {
  let position = 0;
  while (position < value.length && value[position] === "@") {
    const match = /^@([A-Za-z_][A-Za-z0-9_]*) /.exec(value.slice(position));
    if (!match) break;
    position += match[0].length;
  }
  return position;
}

function roleMentionKey(role: SuperRole) {
  const parts = role.name.match(/[A-Za-z_][A-Za-z0-9_]*|[0-9]+/g) ?? [];
  let value = parts.join("_").replace(/^_+|_+$/g, "");
  if (!value) value = role.id;
  if (!/^[A-Za-z_]/.test(value)) value = `_${value}`;
  return value;
}

async function createSessionForRole(role: SuperRole): Promise<string> {
  const session = await agents.create(role.provider || "codex", initialPromptForRole(role), role.cwd, role.model ?? undefined);
  const data = await updateSuperRole(role.id, { session_ref: session.ref });
  roles.value = data.roles;
  const updatedRole = roles.value.find((item) => item.id === role.id);
  if (updatedRole) role.session_ref = updatedRole.session_ref;
  return session.ref;
}

function initialPromptForRole(role: SuperRole) {
  const common = commonPrompt.value.trim();
  const rolePrompt = bootstrapPrompt(role);
  return common ? `${common}\n\n${rolePrompt}` : rolePrompt;
}

function bootstrapPrompt(role: SuperRole) {
  return `You are a persistent Super Workspace role named "${role.name}".

Fixed role rules:
${role.description || "(No role rules were provided.)"}

Operate as this role only. Prefer work that matches the fixed rules, files, topic, and responsibilities above. If a later user message appears unrelated to this role, say so briefly and ask for clarification instead of silently switching tasks.`;
}

function roleSnapshot(role: SuperRole) {
  return role.session_ref ? snapshots.value[role.session_ref] : undefined;
}

function roleStatus(role: SuperRole) {
  return roleSnapshot(role)?.status ?? "idle";
}

function contextPercent(role: SuperRole) {
  const value = Number(roleSnapshot(role)?.raw?.context_used_percent);
  if (!Number.isFinite(value)) return "";
  return `${Math.round(value)}%`;
}

function runTargets(run: SuperHistoryRun) {
  return run.targets;
}

function runHasResponse(run: SuperHistoryRun) {
  return run.targets.some((target) => Boolean(targetFinalMessage(target)));
}

function targetFinalMessage(target: SuperHistoryTarget) {
  return [...target.messages]
    .reverse()
    .find((message) => message.role === "assistant" && message.event_type === "message:assistant" && message.text.trim());
}

function targetHtml(target: SuperHistoryTarget) {
  const text = targetFinalMessage(target)?.text.trim() ?? "";
  return text ? renderMarkdown(text, { baseDirectory: snapshots.value[target.session_ref]?.cwd_relative ?? "" }) : "";
}

function targetIcon(target: SuperHistoryTarget) {
  return agents.providerById(target.provider || "codex").icon;
}

function roleProviderName(role: SuperRole) {
  return agents.providerById(role.provider || "codex").name;
}

function roleIcon(role: SuperRole) {
  return agents.providerById(role.provider || "codex").icon;
}

function roleAbbrev(role: SuperRole) {
  const words = role.name.trim().split(/\s+/).filter(Boolean);
  if (!words.length) return "?";
  const value = words.length === 1 ? words[0].slice(0, 2) : `${words[0][0] ?? ""}${words[1][0] ?? ""}`;
  return value.toUpperCase();
}

function formatTime(value: number) {
  return new Date(value * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

async function loadRuns(reset: boolean) {
  if (reset) historyLoading.value = true;
  else loadingOlder.value = true;
  try {
    const limit = reset ? Math.min(100, Math.max(30, runs.value.length || 30)) : 30;
    const page = await listSuperWorkspaceRuns(limit, reset ? undefined : nextBefore.value ?? undefined);
    if (reset) {
      runs.value = page.runs;
      await scrollThreadToBottom();
    } else {
      const seen = new Set(runs.value.map((run) => run.id));
      runs.value = [...runs.value, ...page.runs.filter((run) => !seen.has(run.id))];
    }
    updateRunsAfterCursor(page.runs, page.next_after ?? undefined);
    hasOlderRuns.value = page.has_more;
    nextBefore.value = page.next_before ?? null;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    historyLoading.value = false;
    loadingOlder.value = false;
  }
}

async function loadChangedRuns() {
  const after = runsAfterCursor.value;
  if (!after) {
    await loadRuns(true);
    return;
  }
  const stickToBottom = isThreadNearBottom();
  try {
    const page = await listSuperWorkspaceRuns(100, undefined, Math.max(0, after - 0.001));
    for (const run of page.runs) {
      upsertRun(run);
    }
    updateRunsAfterCursor(page.runs, page.next_after ?? undefined);
    if (stickToBottom && page.runs.length) await scrollThreadToBottom();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function upsertRun(run: SuperHistoryRun) {
  const index = runs.value.findIndex((item) => item.id === run.id);
  if (index >= 0) runs.value.splice(index, 1, run);
  else runs.value = [run, ...runs.value];
  runs.value.sort((left, right) => right.created_at - left.created_at);
}

function updateRunsAfterCursor(changedRuns: SuperHistoryRun[], nextAfter?: number) {
  const changedMax = Math.max(0, ...changedRuns.map((run) => run.updated_at).filter(Number.isFinite));
  runsAfterCursor.value = Math.max(runsAfterCursor.value, changedMax, Number.isFinite(nextAfter) ? Number(nextAfter) : 0);
}

function isThreadNearBottom() {
  const element = threadRef.value;
  if (!element) return true;
  return element.scrollHeight - element.scrollTop - element.clientHeight < 180;
}

async function scrollThreadToBottom() {
  await nextTick();
  const element = threadRef.value;
  if (!element) return;
  element.scrollTop = element.scrollHeight;
}
</script>

<template>
  <div class="super-page">
    <aside class="super-role-rail" aria-label="Super Workspace roles">
      <div class="super-role-list">
        <div
        v-for="role in roles"
        :key="role.id"
          class="super-role-tile-wrap"
        >
          <button
            class="super-role-tile"
        :class="[{ active: role.id === selectedRoleId, mentioned: mentionedRoleIds.includes(role.id) }, `status-${roleStatus(role)}`]"
            type="button"
            :title="`@${roleMentionKey(role)}`"
            :aria-pressed="mentionedRoleIds.includes(role.id)"
            @click="handleRoleClick(role.id)"
      >
            <i class="bi super-role-icon" :class="roleIcon(role)"></i>
            <span class="super-role-abbrev">{{ roleAbbrev(role) }}</span>
          </button>
        </div>
      </div>
      <button class="super-role-add" type="button" title="Add role" :disabled="roleSaving" @click="addRole">
        <i class="bi bi-plus-lg"></i>
      </button>
    </aside>

    <div v-if="rolePanelOpen" class="super-role-panel-backdrop" @click.self="closeRolePanel">
      <aside class="super-role-panel" aria-label="Role settings">
        <div class="super-panel-head">
          <div>
            <div class="super-section-title">Role Settings</div>
            <div v-if="selectedRole" class="super-panel-title">{{ selectedRole.name }}</div>
          </div>
          <button class="btn btn-sm btn-outline-secondary icon-button" type="button" title="Close" @click="closeRolePanel">
            <i class="bi bi-x-lg"></i>
          </button>
        </div>

        <section class="super-common-prompt">
          <div class="super-section-title">Common Prompt</div>
          <VoiceTextarea
            v-model="commonPrompt"
            context-id="super-workspace:common-prompt"
            placeholder="Shared instructions added before every role prompt when a new session starts."
            min-height="140px"
            :rows="6"
          >
            <template #actions>
              <button class="btn btn-sm btn-outline-primary" type="button" :disabled="commonPromptSaving" @click="saveCommonPrompt">
                <i class="bi bi-save"></i>
                <span>{{ commonPromptSaving ? "Saving" : "Save common prompt" }}</span>
              </button>
            </template>
          </VoiceTextarea>
        </section>

        <section v-if="selectedRole" class="super-role-config">
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
              min-height="220px"
              :rows="10"
            />
          </label>
          <div class="super-role-meta">
            <span>{{ roleProviderName(selectedRole) }}</span>
            <span>{{ roleStatus(selectedRole) }}</span>
            <span v-if="contextPercent(selectedRole)">ctx {{ contextPercent(selectedRole) }}</span>
          </div>
          <div class="super-config-actions">
            <button class="btn btn-sm btn-outline-secondary" type="button" :disabled="roleSaving" @click="saveSelectedRole">Save</button>
            <button class="btn btn-sm btn-outline-primary icon-button" type="button" :disabled="busy" title="Renew session" @click="renewSelectedRole">
              <i class="bi bi-arrow-clockwise"></i>
            </button>
            <button class="btn btn-sm btn-outline-danger" type="button" :disabled="roleSaving" @click="removeSelectedRole">Delete</button>
          </div>
        </section>

        <div v-else class="super-empty">Select a role to edit.</div>
      </aside>
    </div>

    <main class="super-chat">
      <section ref="threadRef" class="super-thread">
        <button v-if="hasOlderRuns" class="btn btn-sm btn-outline-secondary super-load-older" type="button" :disabled="loadingOlder" @click="loadRuns(false)">
          <span>{{ loadingOlder ? "Loading" : "Load older" }}</span>
        </button>
        <article v-for="run in displayRuns" :key="run.id" class="super-run">
          <div class="super-user-message">
            <div class="super-run-time">{{ formatTime(run.created_at) }}</div>
            <div class="super-message-text">{{ run.message }}</div>
            <div class="super-route-line">
              <span v-if="run.status === 'selecting'" class="super-route-pending">selecting role to dispatch...</span>
              <span v-else-if="run.status === 'queued'" class="super-route-pending">queued for role dispatch...</span>
              <span v-else-if="run.status === 'running'" class="super-route-pending">starting role dispatch...</span>
              <span v-else-if="run.status === 'failed' && !runHasResponse(run)" class="super-route-error">dispatch failed: {{ run.error }}</span>
              <template v-else>
                <span class="super-route-label">dispatched to</span>
                <span v-for="target in runTargets(run)" :key="target.id" class="super-route-chip">
                  <i class="bi" :class="targetIcon(target)"></i>
                  {{ target.role_name }}
                </span>
              </template>
            </div>
          </div>
          <div v-for="target in runTargets(run)" :key="`${run.id}:${target.id}`" class="super-role-turn">
            <div class="super-response-role-label">{{ target.role_name }}</div>
            <div class="super-role-response">
              <div v-if="targetHtml(target)" class="markdown-body super-response-body" v-html="targetHtml(target)"></div>
              <div v-else class="super-waiting">Waiting for response</div>
            </div>
          </div>
        </article>
        <div v-if="!runs.length && !historyLoading" class="super-empty-thread">Create roles, write one message, and dispatch it into the persistent role sessions.</div>
        <div v-if="historyLoading && !runs.length" class="super-empty-thread">Loading history</div>
      </section>

      <div class="super-composer">
        <div class="super-composer-card">
          <VoiceTextarea
            v-model="composer"
            context-id="super-workspace:composer"
            placeholder="Message Super Workspace"
            :rows="2"
            min-height="58px"
            @keydown="handleComposerKeydown"
          >
            <template #actions>
              <button class="btn btn-primary super-send-button" type="button" :disabled="!canDispatch" @click="dispatchMessage">
                <i class="bi bi-send"></i>
                <span>{{ dispatchButtonLabel }}</span>
              </button>
            </template>
          </VoiceTextarea>
          <div v-if="error" class="super-error">{{ error }}</div>
        </div>
      </div>
    </main>
  </div>
</template>

<style scoped>
.super-page {
  background: #f6f7f9;
  color: var(--text);
  display: grid;
  grid-template-columns: 52px minmax(0, 1fr);
  height: 100%;
  min-height: 0;
  position: relative;
}

.super-role-rail {
  background: #ffffff;
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-height: 0;
  padding: 7px 5px;
}

.super-config-actions,
.super-composer-actions,
.super-route-line,
.super-role-meta {
  align-items: center;
  display: flex;
  gap: 8px;
}

.super-role-list {
  display: grid;
  gap: 6px;
  min-height: 0;
  overflow: auto;
  padding: 1px;
}

.super-role-tile-wrap {
  height: 38px;
  position: relative;
}

.super-role-tile,
.super-role-add {
  align-items: center;
  background: var(--role-status-bg, transparent);
  border: 1px solid var(--role-status-border, transparent);
  border-radius: 7px;
  box-shadow: inset 3px 0 0 var(--role-status-color, transparent);
  color: inherit;
  display: grid;
  justify-items: center;
  padding: 3px 3px 3px 5px;
  width: 100%;
}

.super-role-tile {
  grid-template-rows: 15px 13px;
  height: 38px;
}

.super-role-tile.active,
.super-role-tile:hover,
.super-role-add:hover {
  background: #eef3ff;
  border-color: #c8d8ff;
}

.super-role-tile.mentioned {
  background: #e8f7ef;
  border-color: #8fd6ad;
}

.super-role-tile.active.mentioned,
.super-role-tile.mentioned:hover {
  background: #dff3ea;
  border-color: #6fc993;
}

.super-role-tile:focus-visible,
.super-role-add:focus-visible {
  outline: 2px solid #86b7fe;
  outline-offset: 2px;
}

.super-role-icon {
  font-size: 13px;
  line-height: 1;
}

.super-role-abbrev {
  font-size: 9px;
  font-weight: 700;
  line-height: 1;
  max-width: 34px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.super-role-add {
  height: 36px;
  margin-top: auto;
}

.super-role-panel-backdrop {
  inset: 0;
  pointer-events: none;
  position: absolute;
  z-index: 20;
}

.super-role-panel {
  background: #ffffff;
  border-left: 1px solid var(--border);
  box-shadow: -12px 0 30px rgba(29, 41, 57, 0.16);
  display: flex;
  flex-direction: column;
  gap: 12px;
  height: 100%;
  margin-left: auto;
  max-width: min(420px, calc(100vw - 52px));
  overflow: auto;
  padding: 12px;
  pointer-events: auto;
  width: 420px;
}

.super-panel-head {
  align-items: center;
  display: flex;
  justify-content: space-between;
}

.super-panel-title {
  font-size: 16px;
  font-weight: 700;
  margin-top: 2px;
}

.super-section-title {
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0;
  text-transform: uppercase;
}

.status-idle,
.status-exited {
  --role-status-bg: #f6f8fb;
  --role-status-border: #d8e0ea;
  --role-status-color: #8ca0bd;
}

.status-running {
  --role-status-bg: #edf5ff;
  --role-status-border: #b8d7ff;
  --role-status-color: #0d6efd;
}

.status-failed {
  --role-status-bg: #fff1f2;
  --role-status-border: #f3b4bc;
  --role-status-color: #dc3545;
}

.super-role-config {
  border-top: 1px solid var(--border);
  padding-top: 12px;
}

.super-common-prompt {
  border-top: 1px solid var(--border);
  display: grid;
  gap: 8px;
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
  grid-template-rows: minmax(0, 1fr) auto;
  min-height: 0;
  min-width: 0;
}

.super-composer {
  background: rgba(246, 247, 249, 0.96);
  border-top: 1px solid var(--border);
  box-shadow: 0 -8px 22px rgba(29, 41, 57, 0.06);
  padding: 10px 14px 12px;
}

.super-composer-card {
  background: #ffffff;
  border: 1px solid var(--border);
  border-radius: 12px;
  box-shadow: 0 4px 14px rgba(29, 41, 57, 0.07);
  display: grid;
  gap: 6px;
  margin: 0 auto;
  max-width: 980px;
  padding: 8px;
}

.super-composer-card :deep(.voice-textarea) {
  gap: 6px;
}

.super-composer-card :deep(textarea) {
  border: 0;
  border-radius: 8px;
  padding: 8px 10px;
  resize: none;
}

.super-composer-card :deep(textarea:focus) {
  box-shadow: none;
}

.super-composer-card :deep(.voice-textarea-actions) {
  align-items: center;
  justify-content: flex-end;
}

.super-send-button {
  border-radius: 999px;
  min-width: 142px;
}

.super-thread {
  min-height: 0;
  min-width: 0;
  overflow: auto;
  padding: 16px 16px 18px;
}

.super-run {
  display: grid;
  gap: 8px;
  margin: 0 auto 18px;
  max-width: 980px;
  min-width: 0;
}

.super-load-older {
  justify-self: center;
  margin: 0 auto 16px;
}

.super-user-message,
.super-role-response {
  background: #ffffff;
  border: 1px solid var(--border);
  border-radius: 8px;
  max-width: 100%;
  min-width: 0;
  padding: 10px 12px;
}

.super-user-message {
  margin-left: auto;
  max-width: min(760px, 100%);
}

.super-run-time,
.super-waiting,
.super-empty,
.super-empty-thread {
  color: var(--text-muted);
  font-size: 12px;
}

.super-message-text {
  overflow-wrap: anywhere;
  white-space: pre-wrap;
  word-break: break-word;
}

.super-route-line {
  flex-wrap: wrap;
  gap: 4px 7px;
  margin-top: 6px;
}

.super-route-chip {
  align-items: center;
  color: var(--text-muted);
  display: inline-flex;
  font-size: 12px;
  gap: 4px;
  line-height: 1.3;
}

.super-route-label,
.super-route-pending {
  color: var(--text-muted);
  font-size: 12px;
}

.super-route-error {
  color: #a33;
  font-size: 12px;
}

.super-role-turn {
  display: grid;
  gap: 4px;
}

.super-response-role-label {
  color: #111827;
  font-size: 12px;
  font-weight: 400;
  line-height: 1.3;
  padding-left: 2px;
}

.super-response-body {
  font-size: var(--markdown-body-font-size);
  min-width: 0;
  overflow-wrap: anywhere;
  word-break: break-word;
}

.super-response-body :deep(pre) {
  max-width: 100%;
  overflow-x: auto;
  white-space: pre;
}

.super-response-body :deep(table) {
  display: block;
  max-width: 100%;
  overflow-x: auto;
}

.super-response-body :deep(img),
.super-response-body :deep(video) {
  height: auto;
  max-width: 100%;
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
    grid-template-columns: 46px minmax(0, 1fr);
  }

  .super-role-rail {
    padding: 6px 4px;
  }

  .super-role-panel {
    max-width: calc(100vw - 46px);
    width: calc(100vw - 46px);
  }

  .super-thread {
    padding: 10px 6px 12px;
  }

  .super-run {
    margin-bottom: 12px;
  }

  .super-user-message,
  .super-role-response {
    padding: 9px 10px;
  }
}
</style>
