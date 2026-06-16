<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import {
  createSuperWorkspaceRun,
  getSuperWorkspace,
  listSuperChats,
  listSuperWorkspaceRuns,
  resolveDirectoryLink,
} from "../api/client";
import { connectSuperWorkspaceEvents } from "../api/events";
import { useAgentsStore } from "../stores/agents";
import { useFilesStore } from "../stores/files";
import { useInputSessionsStore } from "../stores/inputSessions";
import { useLayoutStore } from "../stores/layout";
import { useSuperChatComposerStore } from "../stores/superChatComposer";
import { useSuperChatDispatchStore } from "../stores/superChatDispatch";
import { useVoiceStore } from "../stores/voice";
import type { SuperChatSummary, SuperDisplayItem, SuperDisplayTarget, SuperHistoryRun, SuperRole } from "../types/superWorkspace";
import { renderMarkdown } from "../utils/markdownRender";
import VoiceTextarea from "./VoiceTextarea.vue";

type SuperThreadItem = SuperDisplayItem & { merged_message_ids?: string[] };

const props = defineProps<{ chatId: string; paneId: string }>();
const agents = useAgentsStore();
const files = useFilesStore();
const inputSessions = useInputSessionsStore();
const layout = useLayoutStore();
const composerState = useSuperChatComposerStore();
const dispatchSelection = useSuperChatDispatchStore();
const voice = useVoiceStore();
const roles = ref<SuperRole[]>([]);
const currentChat = ref<SuperChatSummary | null>(null);
const resolvedChatId = ref("");
const items = ref<SuperDisplayItem[]>([]);
const threadRef = ref<HTMLElement | null>(null);
const composerShellRef = ref<HTMLElement | null>(null);
const composerTextareaRef = ref<InstanceType<typeof VoiceTextarea> | null>(null);
const composer = computed({
  get: () => composerState.draft(resolvedChatId.value),
  set: (value: string) => composerState.setDraft(resolvedChatId.value, value),
});
const composerExpanded = ref(false);
const selectedComposerMentionToken = ref("");
const error = ref("");
const busy = ref(false);
const historyLoading = ref(false);
const loadingOlder = ref(false);
const hasOlderRuns = ref(false);
const nextBefore = ref<number | null>(null);
const runsAfterCursor = ref(0);
const composerMentionItems = computed(() => buildComposerMentionItems(composer.value));
const rolesById = computed(() => new Map(roles.value.map((role) => [role.id, role])));
const chatDispatchRoles = computed(() =>
  (currentChat.value?.member_role_ids ?? []).map((roleId) => rolesById.value.get(roleId)).filter((role): role is SuperRole => Boolean(role)),
);
const chatMemberRoleIds = computed(() => new Set(currentChat.value?.member_role_ids ?? []));
const selectedDispatchRoleIds = computed(() => dispatchSelection.selectedRoleIds(resolvedChatId.value));
const selectedDispatchRoles = computed(() => {
  const selected = new Set(selectedDispatchRoleIds.value);
  return chatDispatchRoles.value.filter((role) => selected.has(role.id));
});
const dispatchPickerTitle = computed(() => {
  if (!selectedDispatchRoles.value.length) return "Auto dispatch";
  return `Dispatch to ${selectedDispatchRoles.value.map((role) => role.name).join(", ")}`;
});
const inputContextId = computed(() => `super-workspace:${resolvedChatId.value}:composer`);
const displayItems = computed<SuperThreadItem[]>(() => mergeMessagesByDriverRun([...items.value].reverse()));
const canDispatch = computed(() => Boolean(composer.value.trim()) && !busy.value);
const composerPinned = computed(() => composerState.isPinned(resolvedChatId.value));
const composerCollapsed = computed(() => !composerPinned.value && !composerExpanded.value && !composer.value.trim());
let fallbackTimer: number | null = null;
let refreshTimer: number | null = null;
let superWorkspaceEvents: EventSource | null = null;

onMounted(async () => {
  await agents.loadProviders();
  await Promise.all([loadRoles(), loadChatContext()]);
  await loadRuns(true);
  superWorkspaceEvents = connectSuperWorkspaceEvents((event) => {
    if (event.chat_id && event.chat_id !== resolvedChatId.value) return;
    scheduleRefreshLiveState(100);
  });
  fallbackTimer = window.setInterval(() => scheduleRefreshLiveState(0), 30000);
  window.addEventListener("super-workspace:add-role-mention", handleExternalRoleMention);
  window.addEventListener("super-workspace:chats-updated", handleChatsUpdated);
});

onUnmounted(() => {
  if (fallbackTimer !== null) window.clearInterval(fallbackTimer);
  if (refreshTimer !== null) window.clearTimeout(refreshTimer);
  superWorkspaceEvents?.close();
  window.removeEventListener("super-workspace:add-role-mention", handleExternalRoleMention);
  window.removeEventListener("super-workspace:chats-updated", handleChatsUpdated);
});

watch(() => props.chatId, async () => {
  resolvedChatId.value = "";
  selectedComposerMentionToken.value = "";
  currentChat.value = null;
  items.value = [];
  nextBefore.value = null;
  runsAfterCursor.value = 0;
  await loadChatContext();
  if (resolvedChatId.value) await loadRuns(true);
});

watch([selectedDispatchRoleIds, resolvedChatId, () => currentChat.value?.id], ([roleIds, watchingChatId, currentChatId]) => {
  if (!currentChatId || watchingChatId !== currentChatId) return;
  const dispatchRoleIds = chatMemberRoleIds.value;
  if (!roleIds.length) return;
  if (!dispatchRoleIds.size) {
    dispatchSelection.clearChat(watchingChatId);
    registerInputContext();
    return;
  }
  for (const roleId of roleIds) {
    if (!dispatchRoleIds.has(roleId)) dispatchSelection.clearRole(watchingChatId, roleId);
  }
  registerInputContext();
});

watch(() => resolvedChatId.value, () => registerInputContext(), { immediate: true });

async function loadRoles() {
  const data = await getSuperWorkspace();
  roles.value = data.roles;
}

async function loadChatContext() {
  const data = await listSuperChats();
  const byId = data.chats.find((chat) => chat.id === props.chatId) ?? null;
  if (byId) {
    currentChat.value = byId;
    resolvedChatId.value = byId.id;
    return;
  }
  const byName = data.chats.filter((chat) => chat.name === props.chatId);
  if (byName.length === 1) {
    dispatchSelection.migrateChatId(props.chatId, byName[0].id);
    currentChat.value = byName[0];
    resolvedChatId.value = byName[0].id;
    return;
  }
  resolvedChatId.value = "";
  currentChat.value = null;
}

function hasResolvedChatId(): boolean {
  return Boolean(resolvedChatId.value);
}

function scheduleRefreshLiveState(delayMs: number) {
  if (refreshTimer !== null) return;
  refreshTimer = window.setTimeout(() => {
    refreshTimer = null;
    void refreshLiveState();
  }, delayMs);
}

async function refreshLiveState() {
  await Promise.all([loadRoles(), loadChatContext(), loadChangedRuns()]);
}

function registerInputContext() {
  if (!hasResolvedChatId()) return;
  inputSessions.registerContext({
    id: inputContextId.value,
    kind: "super-chat",
    ownerId: resolvedChatId.value,
    label: "Chat voice input",
    submitTarget: {
      type: "super-chat",
      chatId: resolvedChatId.value,
      roleIds: selectedDispatchRoleIds.value.length ? selectedDispatchRoleIds.value : undefined,
    },
  });
}

async function dispatchMessage() {
  if (!hasResolvedChatId()) return;
  const message = composer.value.trim();
  if (!message || busy.value) return;
  if (voice.isBusy(inputContextId.value)) {
    busy.value = true;
    error.value = "";
    try {
      await inputSessions.requestSend(inputContextId.value);
      await refreshLiveState();
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err);
    } finally {
      busy.value = false;
    }
    return;
  }
  busy.value = true;
  error.value = "";
  composerState.clearDraft(resolvedChatId.value);
  if (!composerPinned.value) composerExpanded.value = false;
  try {
    const run = await createSuperWorkspaceRun({
      message,
      chat_id: resolvedChatId.value,
      role_ids: selectedDispatchRoleIds.value.length ? selectedDispatchRoleIds.value : undefined,
    });
    upsertDisplayItem(displayItemFromRun(run));
    updateItemsAfterCursor([displayItemFromRun(run)]);
    await scrollThreadToBottom();
    if (run.status === "failed") error.value = run.error || "Dispatch failed";
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

function expandComposer() {
  composerExpanded.value = true;
  void nextTick(() => composerTextareaRef.value?.focusVoice());
}

function toggleComposerPinned() {
  const pinned = composerState.togglePinned(resolvedChatId.value);
  if (pinned) composerExpanded.value = true;
}

function isDispatchRoleSelected(roleId: string) {
  return dispatchSelection.isRoleSelected(resolvedChatId.value, roleId);
}

function toggleDispatchRole(roleId: string) {
  dispatchSelection.toggleRole(resolvedChatId.value, roleId);
}

function handleDispatchPickerSummaryClick(event: MouseEvent) {
  if (!selectedDispatchRoleIds.value.length) return;
  event.preventDefault();
  dispatchSelection.clearChat(resolvedChatId.value);
  const details = event.currentTarget instanceof HTMLElement ? event.currentTarget.closest("details") : null;
  if (details instanceof HTMLDetailsElement) details.open = false;
}

function closeDispatchPicker(event: Event) {
  const target = event.currentTarget;
  if (!(target instanceof HTMLDetailsElement)) return;
  target.open = false;
}

function handleDispatchPickerFocusOut(event: FocusEvent) {
  const target = event.currentTarget;
  if (!(target instanceof HTMLDetailsElement)) return;
  const nextTarget = event.relatedTarget instanceof Node ? event.relatedTarget : null;
  if (nextTarget && target.contains(nextTarget)) return;
  window.setTimeout(() => {
    const active = document.activeElement;
    if (active && target.contains(active)) return;
    target.open = false;
  }, 0);
}

function handleComposerFocusOut(event: FocusEvent) {
  if (composerPinned.value) return;
  const nextTarget = event.relatedTarget instanceof Node ? event.relatedTarget : null;
  if (nextTarget && composerShellRef.value?.contains(nextTarget)) return;
  window.setTimeout(() => {
    if (composerPinned.value) return;
    const active = document.activeElement;
    if (active && composerShellRef.value?.contains(active)) return;
    if (!composer.value.trim()) composerExpanded.value = false;
  }, 0);
}

function handleExternalRoleMention(event: Event) {
  const detail = (event as CustomEvent<{ roleId?: string }>).detail;
  if (!detail?.roleId || layout.activePaneId !== props.paneId) return;
  const role = roles.value.find((item) => item.id === detail.roleId);
  if (role) addRoleMention(role);
}

function handleChatsUpdated() {
  void loadChatContext();
}

function addRoleMention(role: SuperRole) {
  const token = `@${roleMentionKey(role)}`;
  if (leadingMentionTokens(composer.value).includes(token)) return;
  const position = leadingMentionPrefixEnd(composer.value);
  composer.value = `${composer.value.slice(0, position)}${token} ${composer.value.slice(position)}`;
  expandComposer();
}

function addMessageCitation(messageId: string) {
  const token = `@msg-${messageId}`;
  if (leadingMentionTokens(composer.value).includes(token)) return;
  const position = leadingMentionPrefixEnd(composer.value);
  composer.value = `${composer.value.slice(0, position)}${token} ${composer.value.slice(position)}`;
  expandComposer();
}

function mergeMessagesByDriverRun(orderedItems: SuperDisplayItem[]): SuperThreadItem[] {
  const merged: SuperThreadItem[] = [];
  const runIndexes = new Map<string, number>();
  for (const item of orderedItems) {
    const key = messageDriverRunKey(item);
    if (!key) {
      merged.push({ ...item, merged_message_ids: item.kind === "message" ? [item.message_id] : undefined });
      continue;
    }

    const existingIndex = runIndexes.get(key);
    if (existingIndex === undefined) {
      runIndexes.set(key, merged.length);
      merged.push({ ...item, merged_message_ids: [item.message_id] });
      continue;
    }
    merged[existingIndex] = appendMergedRunMessage(merged[existingIndex], item);
  }
  return merged;
}

function messageDriverRunKey(item: SuperDisplayItem) {
  if (item.kind !== "message" || !item.driver_run_id) return "";
  return item.driver_run_id;
}

function appendMergedRunMessage(base: SuperThreadItem, item: SuperDisplayItem): SuperThreadItem {
  const ids = [...(base.merged_message_ids ?? [base.message_id])];
  if (!ids.includes(item.message_id)) ids.push(item.message_id);
  return {
    ...base,
    id: `${base.id}:${item.id}`,
    text: [base.text.trimEnd(), item.text.trimStart()].filter(Boolean).join("\n\n"),
    updated_at: Math.max(base.updated_at, item.updated_at),
    message_id: item.message_id,
    viewer_session_id: item.viewer_session_id,
    provider_session_id: item.provider_session_id,
    session_ref: item.session_ref,
    model_context_window: item.model_context_window,
    total_tokens: item.total_tokens,
    context_used_percent: item.context_used_percent,
    target_status: item.target_status,
    merged_message_ids: ids,
  };
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
    if (role) return { ...entry, type: "dispatch", icon: roleIcon(role), label: role.name, detail: entry.token };
    const messageId = citationMessageId(entry.token);
    const cited = messageId ? items.value.find((item) => item.message_id === messageId) : undefined;
    if (messageId) return { ...entry, type: "citation", icon: "bi-link-45deg", label: cited?.role_name || "Message", detail: entry.token };
    return { ...entry, type: "unknown", icon: "bi-at", label: entry.token, detail: "unrecognized" };
  });
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
    entries.push({ token: `@${match[1]}`, start: position, end: position + match[0].length });
    position += match[0].length;
  }
  return entries;
}

function citationMessageId(token: string) {
  return /^@(?:msg|message)-(.+)$/.exec(token)?.[1] ?? "";
}

function roleMentionKey(role: SuperRole) {
  const parts = role.name.match(/[A-Za-z_][A-Za-z0-9_]*|[0-9]+/g) ?? [];
  let value = parts.join("_").replace(/^_+|_+$/g, "");
  if (!value) value = role.id;
  if (!/^[A-Za-z_]/.test(value)) value = `_${value}`;
  return value;
}

function itemBaseDirectory(item: SuperDisplayItem) {
  return typeof item.raw?.cwd_relative === "string" ? item.raw.cwd_relative : "";
}

function itemHtml(item: SuperDisplayItem) {
  return item.text ? renderMarkdown(item.text.trim(), { baseDirectory: itemBaseDirectory(item) }) : "";
}

async function openItemLink(event: MouseEvent, item: SuperDisplayItem) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  const link = (event.target instanceof Element ? event.target : null)?.closest<HTMLAnchorElement>("a[data-viewer-link]");
  const target = link?.dataset.viewerTarget;
  if (!target) return;
  event.preventDefault();
  try {
    const resolved = await resolveDirectoryLink(itemBaseDirectory(item), target);
    await files.recordVisit(resolved.path);
    layout.openFile(resolved.path);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function itemIcon(item: SuperDisplayItem) {
  return agents.providerById(item.provider || "codex").icon;
}

function shortSessionId(item: Pick<SuperDisplayItem, "provider_session_id" | "viewer_session_id" | "session_ref">) {
  const value = item.provider_session_id || item.viewer_session_id || item.session_ref;
  if (!value) return "";
  const raw = value.includes(":") ? value.split(":").pop() || value : value;
  return raw.length > 10 ? raw.slice(0, 10) : raw;
}

function contextUsageLabel(item: Pick<SuperDisplayItem, "context_used_percent" | "total_tokens">) {
  if (typeof item.context_used_percent === "number") return `${item.context_used_percent.toFixed(1)}% ctx`;
  if (typeof item.total_tokens === "number") return `${item.total_tokens.toLocaleString()} tokens`;
  return "";
}

function targetIcon(target: SuperDisplayTarget) {
  return agents.providerById(target.provider || "codex").icon;
}

function roleIcon(role: SuperRole) {
  return agents.providerById(role.provider || "codex").icon;
}

function formatTime(value: number) {
  return new Date(value * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

async function loadRuns(reset: boolean) {
  if (!hasResolvedChatId()) return;
  if (reset) historyLoading.value = true;
  else loadingOlder.value = true;
  const thread = reset ? null : threadRef.value;
  const previousScrollHeight = thread?.scrollHeight ?? 0;
  const previousScrollTop = thread?.scrollTop ?? 0;
  try {
    const limit = reset ? Math.min(100, Math.max(30, items.value.length || 30)) : 30;
    const page = await listSuperWorkspaceRuns(limit, reset ? undefined : nextBefore.value ?? undefined, undefined, resolvedChatId.value);
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
  if (!hasResolvedChatId()) return;
  const after = runsAfterCursor.value;
  if (!after) {
    await loadRuns(true);
    return;
  }
  const stickToBottom = isThreadNearBottom();
  const page = await listSuperWorkspaceRuns(100, undefined, Math.max(0, after - 0.001), resolvedChatId.value);
  for (const item of page.items) upsertDisplayItem(item);
  updateItemsAfterCursor(page.items, page.next_after ?? undefined);
  if (stickToBottom && page.items.length) await scrollThreadToBottom();
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
    workspace_id: run.workspace_id,
    chat_id: run.chat_id,
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
    viewer_session_id: "",
    provider_session_id: null,
    session_ref: "",
    model_context_window: null,
    total_tokens: null,
    context_used_percent: null,
    target_status: "",
    run_status: run.status,
    error: run.error,
    citation_ids: run.citation_ids ?? [],
    dispatch_targets: run.targets.map((target) => ({
      id: target.id,
      workspace_id: target.workspace_id,
      chat_id: target.chat_id,
      role_id: target.role_id,
      role_name: target.role_name,
      provider: target.provider,
      viewer_session_id: target.viewer_session_id,
      provider_session_id: target.provider_session_id ?? null,
      session_ref: target.session_ref,
      status: target.status,
      model_context_window: target.model_context_window ?? null,
      total_tokens: target.total_tokens ?? null,
      context_used_percent: target.context_used_percent ?? null,
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
  if (element) element.scrollTop = element.scrollHeight;
}
</script>

<template>
  <div class="super-chat-pane">
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
            <div class="super-response-meta">
              <span class="super-response-role-label">
                <i class="bi" :class="itemIcon(item)"></i>
                {{ item.role_name }}
              </span>
              <span v-if="shortSessionId(item)" class="super-meta-chip" :title="item.session_ref || item.viewer_session_id">
                session {{ shortSessionId(item) }}
              </span>
              <span v-if="contextUsageLabel(item)" class="super-meta-chip">
                {{ contextUsageLabel(item) }}
              </span>
            </div>
            <button class="super-cite-button" type="button" :title="`Cite @msg-${item.message_id}`" @click="addMessageCitation(item.message_id)">
              <i class="bi bi-link-45deg"></i>
              <span>Cite</span>
            </button>
          </div>
          <div class="super-role-response" @click="openItemLink($event, item)">
            <div class="markdown-body markdown-content super-response-body" v-html="itemHtml(item)"></div>
          </div>
        </div>
      </article>
      <div v-if="!items.length && !historyLoading" class="super-empty-thread">Write one message and dispatch it into this chat.</div>
      <div v-if="historyLoading && !items.length" class="super-empty-thread">Loading history</div>
    </section>

    <div ref="composerShellRef" class="super-composer" :class="{ collapsed: composerCollapsed }" @focusout="handleComposerFocusOut">
      <button
        v-if="composerCollapsed"
        class="super-composer-toggle"
        type="button"
        title="Open message input"
        aria-label="Open message input"
        @click="expandComposer"
      >
        <i class="bi bi-keyboard"></i>
      </button>
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
          ref="composerTextareaRef"
          v-model="composer"
          :context-id="inputContextId"
          placeholder="Message chat"
          :rows="2"
          min-height="58px"
          max-height="50cqh"
          :auto-grow="true"
          @focus="composerExpanded = true"
          @keydown="handleComposerKeydown"
        >
          <template #actions>
            <details
              class="super-dispatch-picker"
              :class="{ active: selectedDispatchRoles.length }"
              :title="dispatchPickerTitle"
              @focusout="handleDispatchPickerFocusOut"
              @keydown.esc.stop.prevent="closeDispatchPicker"
            >
              <summary :aria-label="dispatchPickerTitle" @click="handleDispatchPickerSummaryClick">
                <i class="bi" :class="selectedDispatchRoles.length ? 'bi-people-fill' : 'bi-diagram-3'"></i>
                <span v-if="selectedDispatchRoles.length" class="super-dispatch-count">{{ selectedDispatchRoles.length }}</span>
              </summary>
              <div class="super-dispatch-menu" @mousedown.prevent>
                <button class="super-dispatch-menu-auto" type="button" :class="{ selected: !selectedDispatchRoles.length }" @click="dispatchSelection.clearChat(resolvedChatId)">
                  <i class="bi" :class="selectedDispatchRoles.length ? 'bi-circle' : 'bi-check-circle-fill'"></i>
                  <span>Auto</span>
                </button>
                <div v-if="!chatDispatchRoles.length" class="super-dispatch-empty">No roles in chat</div>
                <button
                  v-for="role in chatDispatchRoles"
                  :key="role.id"
                  class="super-dispatch-option"
                  type="button"
                  :class="{ selected: isDispatchRoleSelected(role.id) }"
                  @click="toggleDispatchRole(role.id)"
                >
                  <i class="bi" :class="isDispatchRoleSelected(role.id) ? 'bi-check-square-fill' : 'bi-square'"></i>
                  <span>{{ role.name }}</span>
                </button>
              </div>
            </details>
            <button class="btn btn-outline-primary voice-action-button super-send-button" type="button" :disabled="!canDispatch" title="Send message (Cmd/Ctrl+Enter)" aria-label="Send message" @click="dispatchMessage">
              <i class="bi bi-send"></i>
            </button>
          </template>
          <template #trailing-actions-after>
            <button
              class="btn btn-sm btn-outline-secondary voice-action-button super-pin-button"
              :class="{ active: composerPinned }"
              type="button"
              :title="composerPinned ? 'Unpin input' : 'Pin input open'"
              :aria-label="composerPinned ? 'Unpin input' : 'Pin input open'"
              :aria-pressed="composerPinned"
              @click="toggleComposerPinned"
            >
              <i class="bi" :class="composerPinned ? 'bi-pin-angle-fill' : 'bi-pin-angle'"></i>
            </button>
          </template>
        </VoiceTextarea>
        <div v-if="error" class="super-error">{{ error }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.super-chat-pane {
  background: #f6f7f9;
  container-type: size;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  position: relative;
}

.super-thread {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 18px;
  min-height: 0;
  min-width: 0;
  overflow-x: hidden;
  overflow-y: auto;
  padding: 18px clamp(14px, 3vw, 34px);
}

.super-run {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.super-user-turn,
.super-role-turn,
.super-message-meta {
  min-width: 0;
}

.super-message-top,
.super-route-line,
.super-response-meta,
.super-composer-mentions,
.super-composer-mention {
  align-items: center;
  display: flex;
}

.super-message-top {
  color: var(--text-muted);
  font-size: 12px;
  justify-content: space-between;
}

.super-route-line,
.super-response-meta,
.super-composer-mentions {
  flex-wrap: wrap;
  gap: 6px;
}

.super-user-message,
.super-role-response {
  background: #ffffff;
  border: 1px solid var(--border);
  border-radius: 8px;
  min-width: 0;
  overflow-x: hidden;
  padding: 12px 14px;
  width: 100%;
}

.super-user-message {
  background: #eaf3ff;
  border-color: #cfe0f7;
}

.super-role-response {
  background: #ffffff;
  overflow-x: auto;
  overflow-y: visible;
}

.super-message-text {
  overflow-wrap: anywhere;
  white-space: pre-wrap;
  word-break: break-word;
}

.super-response-body {
  --markdown-render-body-size: 13px;
  --markdown-render-h1-size: 20px;
  --markdown-render-h2-size: 17px;
  --markdown-render-h3-size: 15px;
  --markdown-render-h4-size: 14px;
  --markdown-render-paragraph-line-height: 1.48;
  --markdown-render-paragraph-size: 13px;
  --markdown-render-pre-padding: 8px;
  max-width: 100%;
  min-width: 0;
  overflow-wrap: anywhere;
  word-break: break-word;
}

.super-response-body :deep(*) {
  max-width: 100%;
}

.super-response-body :deep(a),
.super-response-body :deep(code),
.super-response-body :deep(p),
.super-response-body :deep(li),
.super-response-body :deep(td),
.super-response-body :deep(th) {
  overflow-wrap: anywhere;
  word-break: break-word;
}

.super-response-body :deep(ol),
.super-response-body :deep(ul) {
  margin: 0.25em 0 0.7em;
  padding-left: 1.4em;
}

.super-response-body :deep(ol) {
  list-style: decimal;
  list-style-position: outside;
}

.super-response-body :deep(ul) {
  list-style: disc;
  list-style-position: outside;
}

.super-response-body :deep(ol > li),
.super-response-body :deep(ul > li) {
  display: list-item;
  min-width: 0;
}

.super-response-body :deep(pre) {
  overflow-x: auto;
  white-space: pre;
  max-width: 100%;
}

.super-response-body :deep(table) {
  border-collapse: collapse;
  display: block;
  max-width: 100%;
  overflow: auto;
  width: max-content;
}

.super-response-body :deep(th),
.super-response-body :deep(td) {
  border: 1px solid var(--border);
  padding: 6px 8px;
}

.super-response-body :deep(.markdown-code-line),
.super-response-body :deep(.markdown-code-line-content) {
  min-width: 0;
}

.super-response-role-label {
  color: var(--text);
  display: inline-flex;
  gap: 5px;
  align-items: center;
  font-size: 13px;
  font-weight: 700;
}

.super-meta-chip {
  background: #f4f6f8;
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--text-muted);
  font-size: 11px;
  line-height: 1.2;
  padding: 2px 6px;
}

.super-cite-button,
.super-route-chip,
.super-composer-mention {
  border: 1px solid var(--border);
  border-radius: 7px;
}

.super-cite-button {
  background: #fff;
  color: var(--text-muted);
  display: inline-flex;
  gap: 4px;
  padding: 3px 7px;
}

.super-dispatch-picker {
  align-items: center;
  display: inline-block;
  flex: 0 0 auto;
  height: 32px;
  margin: 0;
  padding: 0;
  position: relative;
  width: 32px;
}

.super-dispatch-picker summary {
  align-items: center;
  background: #ffffff;
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text-muted);
  cursor: pointer;
  display: inline-flex;
  height: 32px;
  justify-content: center;
  list-style: none;
  position: relative;
  width: 32px;
}

.super-dispatch-picker summary::-webkit-details-marker {
  display: none;
}

.super-dispatch-picker.active {
  color: #174ea6;
}

.super-dispatch-picker.active summary {
  background: #e8f1ff;
  border-color: #8db7ff;
}

.super-dispatch-picker .bi {
  font-size: 14px;
  line-height: 1;
  pointer-events: none;
}

.super-dispatch-count {
  align-items: center;
  background: #174ea6;
  border-radius: 999px;
  color: #ffffff;
  display: inline-flex;
  font-size: 9px;
  height: 14px;
  justify-content: center;
  line-height: 1;
  min-width: 14px;
  padding: 0 4px;
  position: absolute;
  right: -4px;
  top: -5px;
}

.super-dispatch-menu {
  background: #ffffff;
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 14px 30px rgb(15 23 42 / 0.18);
  bottom: calc(100% + 6px);
  display: grid;
  gap: 3px;
  left: 0;
  max-height: min(260px, 42vh);
  min-width: 180px;
  overflow-y: auto;
  padding: 6px;
  position: absolute;
  z-index: 30;
}

.super-dispatch-menu-auto,
.super-dispatch-option {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: 6px;
  color: var(--text);
  display: flex;
  font-size: 12px;
  gap: 7px;
  min-width: 0;
  padding: 6px 7px;
  text-align: left;
  width: 100%;
}

.super-dispatch-menu-auto:hover,
.super-dispatch-option:hover {
  background: #f2f6fb;
}

.super-dispatch-menu-auto.selected {
  color: #174ea6;
  font-weight: 700;
}

.super-dispatch-option.selected {
  color: #174ea6;
  font-weight: 700;
}

.super-dispatch-option .bi {
  flex: 0 0 auto;
  pointer-events: none;
}

.super-dispatch-option span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.super-dispatch-empty {
  color: var(--text-muted);
  font-size: 12px;
  padding: 7px;
}

.super-route-chip {
  background: #eef3ff;
  color: #34507a;
  display: inline-flex;
  gap: 4px;
  padding: 2px 6px;
}

.super-route-pending {
  color: #6b5b15;
}

.super-route-error,
.super-error {
  color: #a33;
}

.super-composer {
  flex: 0 0 auto;
  min-width: 0;
  padding: 10px clamp(14px, 3vw, 34px) 14px;
  width: 100%;
  z-index: 5;
}

.super-composer.collapsed {
  bottom: max(14px, env(safe-area-inset-bottom));
  padding: 0;
  position: absolute;
  right: clamp(14px, 3vw, 34px);
  width: auto;
}

.super-composer-card {
  background: #ffffff;
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 8px;
  width: 100%;
}

.super-composer.collapsed .super-composer-card {
  display: none;
}

.super-composer-toggle {
  align-items: center;
  background: #ffffff;
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 10px 24px rgb(15 23 42 / 0.16);
  color: #34507a;
  display: inline-flex;
  height: 42px;
  justify-content: center;
  padding: 0;
  width: 42px;
}

.super-composer-toggle:hover {
  background: #edf4ff;
  border-color: #b7cef6;
  color: #174ea6;
}

.super-composer-toggle .bi {
  font-size: 18px;
  line-height: 1;
}

.super-pin-button.active {
  background: #e8f1ff;
  border-color: #8db7ff;
  color: #174ea6;
}

.super-composer-mentions {
  margin-bottom: 8px;
}

.super-composer-mention {
  background: #f8fafc;
  color: var(--text);
  gap: 5px;
  padding: 3px 7px;
}

.super-composer-mention.selected {
  background: #fff1f1;
  border-color: #d99;
}

.super-composer-mention-token {
  color: var(--text-muted);
  font-size: 11px;
}

.super-empty-thread,
.super-history-boundary {
  color: var(--text-muted);
  padding: 24px;
  text-align: center;
}

@media (max-width: 767.98px) {
  .super-thread {
    gap: 8px;
    padding: 6px;
  }

  .super-run {
    gap: 4px;
  }

  .super-message-top {
    font-size: 10.5px;
    gap: 6px;
  }

  .super-response-body {
    --markdown-render-body-size: 12.5px;
    --markdown-render-h1-size: 17px;
    --markdown-render-h2-size: 15px;
    --markdown-render-h3-size: 13.5px;
    --markdown-render-h4-size: 13px;
    --markdown-render-paragraph-line-height: 1.42;
    --markdown-render-paragraph-size: 12.5px;
    --markdown-render-pre-padding: 6px;
  }

  .super-route-line,
  .super-composer-mentions {
    gap: 4px;
  }

  .super-user-message,
  .super-role-response {
    border-radius: 7px;
    font-size: 12.5px;
    line-height: 1.45;
    padding: 7px 8px;
  }

  .super-response-role-label {
    font-size: 11.5px;
  }

  .super-cite-button {
    padding: 2px 5px;
  }

  .super-route-chip {
    padding: 2px 5px;
  }

  .super-composer {
    padding: 6px;
  }

  .super-composer.collapsed {
    bottom: max(6px, env(safe-area-inset-bottom));
    right: 6px;
  }

  .super-composer-card {
    border-radius: 7px;
    padding: 6px;
  }

  .super-empty-thread,
  .super-history-boundary {
    font-size: 12px;
    padding: 12px 8px;
  }
}
</style>
