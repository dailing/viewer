<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from "vue";
import {
  getSuperRoleStatuses,
  createSuperRole,
  createSuperWorkspaceRun,
  deleteSuperRole,
  getSuperWorkspace,
  listSuperWorkspaces,
  listSuperWorkspaceRuns,
  resolveDirectoryLink,
  updateSuperWorkspace,
  updateSuperRole,
} from "../api/client";
import { connectSuperWorkspaceEvents } from "../api/events";
import DirectoryPicker from "./DirectoryPicker.vue";
import LocalFilePreview from "./LocalFilePreview.vue";
import VoiceTextarea from "./VoiceTextarea.vue";
import { useAgentsStore } from "../stores/agents";
import { useFilesStore } from "../stores/files";
import { useLayoutStore } from "../stores/layout";
import type { AgentSessionSnapshot } from "../types/agents";
import type { SplitDirection } from "../types/layout";
import type { SuperDisplayItem, SuperDisplayTarget, SuperHistoryRun, SuperRole, SuperRoleStatus, SuperWorkspaceSummary } from "../types/superWorkspace";
import { parseAgentRef } from "../utils/agents";
import { renderMarkdown } from "../utils/markdownRender";

type SuperThreadItem = SuperDisplayItem & {
  merged_message_ids?: string[];
};

const agents = useAgentsStore();
const files = useFilesStore();
const layout = useLayoutStore();
const workspaces = ref<SuperWorkspaceSummary[]>([]);
const activeWorkspaceId = ref("");
const roles = ref<SuperRole[]>([]);
const selectedRoleId = ref("");
const snapshots = ref<Record<string, AgentSessionSnapshot>>({});
const roleStatuses = ref<Record<string, SuperRoleStatus>>({});
const items = ref<SuperDisplayItem[]>([]);
const threadRef = ref<HTMLElement | null>(null);
const previewPath = ref<string | null>(null);
const stoppingRoleIds = ref<Set<string>>(new Set());
const composer = ref("");
const selectedComposerMentionToken = ref("");
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
const composerMentionItems = computed(() => buildComposerMentionItems(composer.value));
const displayItems = computed<SuperThreadItem[]>(() => mergeConsecutiveRoleMessages([...items.value].reverse()));
const canDispatch = computed(() => {
  if (!composer.value.trim() || busy.value) return false;
  return true;
});
let fallbackTimer: number | null = null;
let refreshTimer: number | null = null;
let superWorkspaceEvents: EventSource | null = null;

onMounted(async () => {
  await agents.loadProviders();
  await loadWorkspaceState();
  await load();
  await Promise.all([loadRuns(true), refreshRoleStatuses()]);
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
  activeWorkspaceId.value = data.id || activeWorkspaceId.value;
  commonPrompt.value = data.common_prompt ?? "";
  roles.value = data.roles;
  if (!selectedRoleId.value || !roles.value.some((role) => role.id === selectedRoleId.value)) {
    selectedRoleId.value = "";
    rolePanelOpen.value = false;
  }
  await refreshSnapshots();
}

async function refreshLiveState() {
  await Promise.all([refreshSnapshots(), refreshRoleStatuses()]);
  await loadChangedRuns();
}

async function loadWorkspaceState() {
  const data = await listSuperWorkspaces();
  workspaces.value = data.workspaces;
  activeWorkspaceId.value = data.active_workspace_id;
}

async function refreshRoleStatuses() {
  if (!activeWorkspaceId.value) return;
  const data = await getSuperRoleStatuses(activeWorkspaceId.value);
  if (data.workspace_id !== activeWorkspaceId.value) return;
  roleStatuses.value = Object.fromEntries(data.items.map((item) => [item.role_id, item]));
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
    await refreshRoleStatuses();
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
    await refreshRoleStatuses();
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
    await refreshRoleStatuses();
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

async function stopSelectedRole() {
  if (!selectedRole.value) return;
  await stopRole(selectedRole.value);
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

function roleStopSessionRef(role: SuperRole) {
  const activeStatuses = new Set(["claimed", "running"]);
  for (const item of items.value) {
    for (const target of item.dispatch_targets) {
      if (target.role_id === role.id && activeStatuses.has(target.status) && target.session_ref) return target.session_ref;
    }
    if (item.role_id === role.id && activeStatuses.has(item.target_status) && item.session_ref) return item.session_ref;
  }
  return roleStatus(role) === "busy" ? role.session_ref : "";
}

function canStopRole(role: SuperRole | null) {
  return Boolean(role && roleStopSessionRef(role) && !stoppingRoleIds.value.has(role.id));
}

async function stopRole(role: SuperRole) {
  const sessionRef = roleStopSessionRef(role);
  if (!sessionRef || stoppingRoleIds.value.has(role.id)) return;
  stoppingRoleIds.value = new Set([...stoppingRoleIds.value, role.id]);
  error.value = "";
  try {
    await agents.terminate(sessionRef);
    markRoleStopped(role.id);
    await refreshSnapshots();
    await loadChangedRuns();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    const next = new Set(stoppingRoleIds.value);
    next.delete(role.id);
    stoppingRoleIds.value = next;
  }
}

function markRoleStopped(roleId: string) {
  items.value = items.value.map((item) => ({
    ...item,
    target_status: item.role_id === roleId && ["claimed", "running"].includes(item.target_status) ? "cancelled" : item.target_status,
    dispatch_targets: item.dispatch_targets.map((target) =>
      target.role_id === roleId && ["claimed", "running"].includes(target.status) ? { ...target, status: "cancelled" } : target,
    ),
  }));
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
    upsertDisplayItem(displayItemFromRun(run));
    updateItemsAfterCursor([displayItemFromRun(run)]);
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

function addMessageCitation(messageId: string) {
  addMessageCitations([messageId]);
}

function addMessageCitations(messageIds: string[]) {
  const existing = new Set(leadingMentionTokens(composer.value));
  const tokens = [...new Set(messageIds)].map((messageId) => `@msg-${messageId}`).filter((token) => !existing.has(token));
  if (!tokens.length) return;
  const insert = `${tokens.join(" ")} `;
  const position = leadingMentionPrefixEnd(composer.value);
  composer.value = `${composer.value.slice(0, position)}${insert}${composer.value.slice(position)}`;
}

function citationIdsForItem(item: SuperThreadItem) {
  return item.merged_message_ids?.length ? item.merged_message_ids : [item.message_id];
}

function mergeConsecutiveRoleMessages(orderedItems: SuperDisplayItem[]): SuperThreadItem[] {
  const merged: SuperThreadItem[] = [];
  for (const item of orderedItems) {
    const last = merged[merged.length - 1];
    if (last && canMergeRoleMessages(last, item)) {
      const messageIds = [...citationIdsForItem(last)];
      if (!messageIds.includes(item.message_id)) messageIds.push(item.message_id);
      merged[merged.length - 1] = {
        ...last,
        id: `${last.id}:${item.id}`,
        text: [last.text.trimEnd(), item.text.trimStart()].filter(Boolean).join("\n\n"),
        updated_at: Math.max(last.updated_at, item.updated_at),
        message_id: item.message_id,
        merged_message_ids: messageIds,
      };
      continue;
    }
    merged.push({ ...item, merged_message_ids: item.kind === "message" ? [item.message_id] : undefined });
  }
  return merged;
}

function canMergeRoleMessages(left: SuperThreadItem, right: SuperDisplayItem) {
  return left.kind === "message" && right.kind === "message" && roleMessageMergeKey(left) === roleMessageMergeKey(right);
}

function roleMessageMergeKey(item: Pick<SuperDisplayItem, "role_id" | "role_name" | "provider" | "session_ref">) {
  return item.role_id || `${item.provider}:${item.session_ref}:${item.role_name}`;
}

function handleComposerMentionClick(token: string) {
  if (selectedComposerMentionToken.value === token) {
    removeComposerMention(token);
    selectedComposerMentionToken.value = "";
    return;
  }
  selectedComposerMentionToken.value = token;
}

function removeComposerMention(token: string) {
  const entry = leadingMentionEntries(composer.value).find((item) => item.token === token);
  if (!entry) return;
  composer.value = `${composer.value.slice(0, entry.start)}${composer.value.slice(entry.end)}`;
}

function buildComposerMentionItems(value: string) {
  return leadingMentionEntries(value).map((entry) => {
    const role = roles.value.find((item) => `@${roleMentionKey(item)}` === entry.token);
    if (role) {
      return {
        ...entry,
        type: "dispatch",
        icon: roleIcon(role),
        label: role.name,
        detail: entry.token,
      };
    }
    const messageId = citationMessageId(entry.token);
    const citedItem = messageId ? items.value.find((item) => item.message_id === messageId) : undefined;
    if (messageId) {
      return {
        ...entry,
        type: "citation",
        icon: "bi-link-45deg",
        label: citedItem?.kind === "message" ? citedItem.role_name || "Role message" : "User message",
        detail: entry.token,
      };
    }
    return {
      ...entry,
      type: "unknown",
      icon: "bi-at",
      label: entry.token,
      detail: "unrecognized",
    };
  });
}

function parseLeadingMentionRoleIds(value: string) {
  const byKey = new Map(roles.value.map((role) => [roleMentionKey(role), role]));
  const ids: string[] = [];
  for (const token of leadingMentionTokens(value)) {
    const role = byKey.get(token.slice(1));
    if (role && !ids.includes(role.id)) ids.push(role.id);
  }
  return ids;
}

function leadingMentionPrefixEnd(value: string) {
  let position = 0;
  while (position < value.length && value[position] === "@") {
    const match = /^@\S+(?:\s+|$)/.exec(value.slice(position));
    if (!match) break;
    position += match[0].length;
  }
  return position;
}

function leadingMentionTokens(value: string) {
  return leadingMentionEntries(value).map((entry) => entry.token);
}

function leadingMentionEntries(value: string) {
  const entries: Array<{ token: string; start: number; end: number }> = [];
  let position = 0;
  while (position < value.length && value[position] === "@") {
    const match = /^@(\S+)(?:\s+|$)/.exec(value.slice(position));
    if (!match) break;
    const token = `@${match[1]}`;
    entries.push({ token, start: position, end: position + match[0].length });
    position += match[0].length;
  }
  return entries;
}

function citationMessageId(token: string) {
  const match = /^@(?:msg|message)-(.+)$/.exec(token);
  return match?.[1] ?? "";
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
  return roleStatuses.value[role.id]?.status ?? normalizeRoleStatus(roleSnapshot(role)?.status ?? "idle");
}

function normalizeRoleStatus(status: string) {
  if (status === "claimed" || status === "queued" || status === "running") return "busy";
  if (status === "failed") return "failed";
  return status || "idle";
}

function contextPercent(role: SuperRole) {
  const value = Number(roleSnapshot(role)?.raw?.context_used_percent);
  if (!Number.isFinite(value)) return "";
  return `${Math.round(value)}%`;
}

function itemBaseDirectory(item: SuperDisplayItem) {
  return snapshots.value[item.session_ref]?.cwd_relative ?? "";
}

function itemHtml(item: SuperDisplayItem) {
  return item.text ? renderMarkdown(item.text.trim(), { baseDirectory: itemBaseDirectory(item) }) : "";
}

async function openItemLink(event: MouseEvent, item: SuperDisplayItem) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  const element = event.target instanceof Element ? event.target : null;
  const link = element?.closest<HTMLAnchorElement>("a[data-viewer-link]");
  const target = link?.dataset.viewerTarget;
  if (!target) return;
  event.preventDefault();
  error.value = "";
  try {
    const resolved = await resolveDirectoryLink(itemBaseDirectory(item), target);
    await files.recordVisit(resolved.path);
    previewPath.value = resolved.path;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function openPreviewInPane(path: string) {
  previewPath.value = null;
  void files.recordVisit(path);
  layout.openFile(path);
}

function openPreviewInSplit(path: string, direction: SplitDirection) {
  previewPath.value = null;
  void files.recordVisit(path);
  layout.openFileInSplit(path, direction);
}

function itemIcon(item: SuperDisplayItem) {
  return agents.providerById(item.provider || "codex").icon;
}

function targetIcon(target: SuperDisplayTarget) {
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
  const thread = reset ? null : threadRef.value;
  const previousScrollHeight = thread?.scrollHeight ?? 0;
  const previousScrollTop = thread?.scrollTop ?? 0;
  try {
    const limit = reset ? Math.min(100, Math.max(30, items.value.length || 30)) : 30;
    const page = await listSuperWorkspaceRuns(limit, reset ? undefined : nextBefore.value ?? undefined);
    if (reset) {
      items.value = page.items;
      await scrollThreadToBottom();
    } else {
      const seen = new Set(items.value.map((item) => item.id));
      items.value = [...items.value, ...page.items.filter((item) => !seen.has(item.id))];
      await nextTick();
      if (thread) thread.scrollTop = thread.scrollHeight - previousScrollHeight + previousScrollTop;
    }
    updateItemsAfterCursor(page.items, page.next_after ?? undefined);
    hasOlderRuns.value = page.has_more;
    nextBefore.value = page.next_before ?? null;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    historyLoading.value = false;
    loadingOlder.value = false;
  }
}

function handleThreadScroll() {
  const element = threadRef.value;
  if (!element || element.scrollTop > 96 || historyLoading.value || loadingOlder.value || !hasOlderRuns.value) return;
  void loadRuns(false);
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
    for (const item of page.items) {
      upsertDisplayItem(item);
    }
    updateItemsAfterCursor(page.items, page.next_after ?? undefined);
    if (stickToBottom && page.items.length) await scrollThreadToBottom();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function upsertDisplayItem(item: SuperDisplayItem) {
  const index = items.value.findIndex((existing) => existing.id === item.id);
  if (index >= 0) items.value.splice(index, 1, item);
  else items.value = [item, ...items.value];
  items.value.sort((left, right) => right.created_at - left.created_at);
}

function updateItemsAfterCursor(changedItems: SuperDisplayItem[], nextAfter?: number) {
  const changedMax = Math.max(0, ...changedItems.map((item) => item.updated_at).filter(Number.isFinite));
  runsAfterCursor.value = Math.max(runsAfterCursor.value, changedMax, Number.isFinite(nextAfter) ? Number(nextAfter) : 0);
}

function displayItemFromRun(run: SuperHistoryRun): SuperDisplayItem {
  return {
    id: run.id,
    kind: "query",
    user_id: run.user_id,
    text: run.message,
    role: "user",
    event_type: "message:query",
    provider: "",
    created_at: run.created_at,
    updated_at: run.updated_at,
    message_id: run.message_id,
    query_message_id: run.message_id,
    driver_run_id: null,
    parent_message_id: run.parent_message_id ?? null,
    sender_role_id: run.sender_role_id ?? null,
    recipient_role_id: null,
    role_id: run.sender_role_id ?? "user",
    role_name: "",
    session_ref: "",
    target_status: "",
    run_status: run.status,
    error: run.error,
    citation_ids: run.citation_ids ?? [],
    dispatch_targets: run.targets.map((target) => ({
      id: target.id,
      role_id: target.role_id,
      role_name: target.role_name,
      provider: target.provider,
      session_ref: target.session_ref,
      status: target.status,
    })),
    raw: {},
  };
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
            :title="`@${roleMentionKey(role)} · ${roleStatus(role)}`"
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
            <button class="btn btn-sm btn-outline-danger" type="button" :disabled="!canStopRole(selectedRole)" @click="stopSelectedRole">
              <i class="bi bi-stop-fill"></i>
              <span>{{ selectedRole && stoppingRoleIds.has(selectedRole.id) ? "Stopping" : "Stop" }}</span>
            </button>
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
      <section ref="threadRef" class="super-thread" @scroll.passive="handleThreadScroll">
        <div v-if="items.length && (loadingOlder || !hasOlderRuns)" class="super-history-boundary">
          <span>{{ loadingOlder ? "Loading older messages" : "No more messages" }}</span>
        </div>
        <article v-for="item in displayItems" :key="item.id" class="super-run">
          <div v-if="item.kind === 'query'" class="super-user-turn">
            <div class="super-message-top super-user-message-top">
              <div class="super-message-meta">
                <div class="super-run-time">{{ formatTime(item.created_at) }}</div>
                <div class="super-route-line">
                  <span v-if="item.run_status === 'selecting'" class="super-route-pending">selecting role to dispatch...</span>
                  <span v-else-if="item.run_status === 'queued'" class="super-route-pending">queued for role dispatch...</span>
                  <span v-else-if="item.run_status === 'running'" class="super-route-pending">role running...</span>
                  <span v-else-if="item.run_status === 'failed'" class="super-route-error">dispatch failed: {{ item.error }}</span>
                  <template v-else>
                    <span class="super-route-label">dispatched to</span>
                    <span v-for="target in item.dispatch_targets" :key="target.id" class="super-route-chip">
                      <i class="bi" :class="targetIcon(target)"></i>
                      {{ target.role_name }}
                    </span>
                  </template>
                </div>
              </div>
              <button class="super-cite-button" type="button" :title="`Cite @msg-${item.message_id}`" @click="addMessageCitation(item.message_id)">
                <i class="bi bi-link-45deg"></i>
                <span>Cite</span>
              </button>
            </div>
            <div class="super-user-message">
              <div class="super-message-text">{{ item.text }}</div>
            </div>
          </div>
          <div v-else class="super-role-turn">
            <div class="super-message-top">
              <div class="super-response-role-label">
                <i class="bi" :class="itemIcon(item)"></i>
                {{ item.role_name }}
              </div>
              <button class="super-cite-button" type="button" :title="`Cite @msg-${citationIdsForItem(item).join(' @msg-')}`" @click="addMessageCitations(citationIdsForItem(item))">
                <i class="bi bi-link-45deg"></i>
                <span>Cite</span>
              </button>
            </div>
            <div class="super-role-response" @click="openItemLink($event, item)">
              <div class="markdown-body super-response-body" v-html="itemHtml(item)"></div>
            </div>
          </div>
        </article>
        <div v-if="!items.length && !historyLoading" class="super-empty-thread">Create roles, write one message, and dispatch it into the persistent role sessions.</div>
        <div v-if="historyLoading && !items.length" class="super-empty-thread">Loading history</div>
      </section>

      <div class="super-composer">
        <div class="super-composer-card">
          <div v-if="composerMentionItems.length" class="super-composer-mentions" aria-label="Composer mentions">
            <button
              v-for="mention in composerMentionItems"
              :key="mention.token"
              class="super-composer-mention"
              :class="[`type-${mention.type}`, { selected: selectedComposerMentionToken === mention.token }]"
              type="button"
              :title="selectedComposerMentionToken === mention.token ? `Remove ${mention.detail}` : `Select ${mention.detail}`"
              @click="handleComposerMentionClick(mention.token)"
            >
              <i class="bi" :class="mention.icon"></i>
              <span class="super-composer-mention-label">{{ mention.label }}</span>
              <span class="super-composer-mention-token">{{ mention.detail }}</span>
              <i v-if="selectedComposerMentionToken === mention.token" class="bi bi-x-lg super-composer-mention-remove"></i>
            </button>
          </div>
          <VoiceTextarea
            v-model="composer"
            context-id="super-workspace:composer"
            placeholder="Message Super Workspace"
            :rows="2"
            min-height="58px"
            @keydown="handleComposerKeydown"
          >
            <template #actions>
              <button
                class="btn btn-outline-primary voice-action-button super-send-button"
                type="button"
                :disabled="!canDispatch"
                title="Send message (Cmd/Ctrl+Enter)"
                aria-label="Send message"
                @click="dispatchMessage"
              >
                <i class="bi bi-send"></i>
              </button>
            </template>
          </VoiceTextarea>
          <div v-if="error" class="super-error">{{ error }}</div>
        </div>
      </div>
    </main>
    <LocalFilePreview
      v-if="previewPath"
      :path="previewPath"
      @close="previewPath = null"
      @open-pane="openPreviewInPane"
      @open-split="openPreviewInSplit"
    />
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

.status-busy {
  --role-status-bg: #edf5ff;
  --role-status-border: #b8d7ff;
  --role-status-color: #0d6efd;
}

.super-role-tile.status-busy,
.super-role-tile.status-busy.active,
.super-role-tile.status-busy.mentioned,
.super-role-tile.status-busy.active.mentioned,
.super-role-tile.status-busy:hover {
  background: #e8f3ff;
  border-color: #7fb9ff;
  box-shadow: inset 6px 0 0 #0d6efd, 0 0 0 1px rgba(13, 110, 253, 0.12);
}

.super-role-tile.status-busy .super-role-icon,
.super-role-tile.status-busy .super-role-abbrev {
  color: #0b5ed7;
}

.status-failed {
  --role-status-bg: #fff1f2;
  --role-status-border: #f3b4bc;
  --role-status-color: #dc3545;
}

.super-role-tile.status-failed,
.super-role-tile.status-failed.active,
.super-role-tile.status-failed.mentioned,
.super-role-tile.status-failed.active.mentioned,
.super-role-tile.status-failed:hover {
  background: #fff1f2;
  border-color: #ef8f9b;
  box-shadow: inset 6px 0 0 #dc3545, 0 0 0 1px rgba(220, 53, 69, 0.12);
}

.super-role-tile.status-failed .super-role-icon,
.super-role-tile.status-failed .super-role-abbrev {
  color: #b02a37;
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

.super-composer-mentions {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
  min-width: 0;
}

.super-composer-mention {
  align-items: center;
  background: #f7f9fc;
  border: 1px solid #dbe3ee;
  border-radius: 7px;
  color: #243041;
  display: inline-flex;
  font-size: 11px;
  gap: 5px;
  line-height: 1.2;
  max-width: min(100%, 360px);
  min-height: 24px;
  min-width: 0;
  padding: 3px 7px;
}

.super-composer-mention.type-dispatch {
  background: #edf7f2;
  border-color: #b8dfc8;
}

.super-composer-mention.type-citation {
  background: #f4f7ff;
  border-color: #cbd8f5;
}

.super-composer-mention.selected {
  background: #fff7ed;
  border-color: #f0b36c;
  color: #7a3d08;
}

.super-composer-mention:hover,
.super-composer-mention:focus-visible {
  border-color: #7ea3d8;
}

.super-composer-mention-label,
.super-composer-mention-token {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.super-composer-mention-label {
  font-weight: 600;
  min-width: 0;
}

.super-composer-mention-token {
  color: var(--text-muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  min-width: 34px;
}

.super-composer-mention-remove {
  font-size: 10px;
  line-height: 1;
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
  flex: 0 0 auto;
}

.super-thread {
  -webkit-overflow-scrolling: touch;
  min-height: 0;
  min-width: 0;
  overscroll-behavior: contain;
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

.super-history-boundary {
  align-items: center;
  color: var(--text-muted);
  display: flex;
  font-size: 12px;
  gap: 10px;
  justify-content: center;
  margin: 0 auto 16px;
  max-width: 520px;
}

.super-history-boundary::before,
.super-history-boundary::after {
  background: #d7dde8;
  content: "";
  flex: 1 1 auto;
  height: 1px;
  min-width: 24px;
}

.super-history-boundary span {
  flex: 0 0 auto;
}

.super-user-message,
.super-role-response {
  border: 1px solid var(--border);
  border-radius: 8px;
  max-width: 100%;
  min-width: 0;
  padding: 8px 10px;
}

.super-user-message {
  background: #eef8f3;
  border-color: #cfe8d9;
  margin-left: auto;
  max-width: min(760px, 100%);
}

.super-role-response {
  background: #ffffff;
}

.super-user-turn {
  display: grid;
  gap: 3px;
  justify-items: end;
}

.super-user-message-top {
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

.super-message-top {
  align-items: center;
  display: flex;
  gap: 8px;
  justify-content: space-between;
  min-width: 0;
}

.super-message-meta {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 3px 8px;
  min-width: 0;
}

.super-cite-button {
  align-items: center;
  background: transparent;
  border: 0;
  color: var(--text-muted);
  display: inline-flex;
  font-size: 11px;
  gap: 3px;
  line-height: 1.2;
  padding: 2px 0;
}

.super-cite-button:hover,
.super-cite-button:focus-visible {
  color: #1d4ed8;
}

.super-route-line {
  flex-wrap: wrap;
  gap: 4px 7px;
  margin-top: 0;
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
  gap: 3px;
}

.super-response-role-label {
  align-items: center;
  color: #111827;
  display: inline-flex;
  font-size: 12px;
  font-weight: 400;
  gap: 4px;
  line-height: 1.3;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.super-response-body {
  font-size: var(--markdown-body-font-size);
  min-width: 0;
  overflow-wrap: anywhere;
  word-break: break-word;
}

.super-response-body :deep(> :first-child) {
  margin-top: 0;
}

.super-response-body :deep(> :last-child) {
  margin-bottom: 0;
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
