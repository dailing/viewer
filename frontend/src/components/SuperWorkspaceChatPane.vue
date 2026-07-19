<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import {
  createSuperWorkspaceRun,
  getSuperWorkspace,
  listSuperChats,
  listSuperWorkspaceRuns,
  resolveDirectoryLink,
  stopSuperWorkspaceTarget,
} from "../api/client";
import { connectSuperWorkspaceEvents } from "../api/events";
import { useAgentsStore } from "../stores/agents";
import { useFilesStore } from "../stores/files";
import { useInputSessionsStore } from "../stores/inputSessions";
import { useLayoutStore } from "../stores/layout";
import { usePaneToolbarStore } from "../stores/paneToolbar";
import { useSuperChatComposerStore } from "../stores/superChatComposer";
import { useSuperChatDispatchStore } from "../stores/superChatDispatch";
import { useVoiceStore } from "../stores/voice";
import type { SuperChatSummary, SuperDisplayItem, SuperDisplayTarget, SuperHistoryRun, SuperRole } from "../types/superWorkspace";
import { renderMarkdown } from "../utils/markdownRender";
import VoiceTextarea from "./VoiceTextarea.vue";

type SuperThreadItem = SuperDisplayItem & { merged_message_ids?: string[] };
type SuperChatCachePayload = { chats: SuperChatSummary[]; activeChatId: string };

const props = defineProps<{ chatId: string; paneId: string }>();
const agents = useAgentsStore();
const files = useFilesStore();
const inputSessions = useInputSessionsStore();
const layout = useLayoutStore();
const paneToolbar = usePaneToolbarStore();
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
const chatCache = ref<SuperChatSummary[]>([]);
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
const displayItems = computed<SuperThreadItem[]>(() => mergeMessagesByDriverRun(withTargetPlaceholders([...items.value].reverse())));
const renderedItemHtmlById = computed(() => {
  const rendered = new Map<string, string>();
  for (const item of displayItems.value) {
    if (item.kind !== "message" || !item.text) continue;
    rendered.set(item.id, renderMarkdown(item.text.trim(), { baseDirectory: itemBaseDirectory(item) }));
  }
  return rendered;
});
const canDispatch = computed(() => Boolean(composer.value.trim()) && !busy.value);
const composerPinned = computed(() => composerState.isPinned(resolvedChatId.value));
const composerCollapsed = computed(() => !composerPinned.value && !composerExpanded.value && !composer.value.trim());
const citationPreview = ref<{
  messageId: string;
  title: string;
  text: string;
  top: number;
  left: number;
} | null>(null);
let fallbackTimer: number | null = null;
let refreshTimer: number | null = null;
let citationTimer: number | null = null;
let superWorkspaceEvents: EventSource | null = null;
let chatLoadRequest = 0;
const jumpedMessageId = ref("");
const stopConfirmationId = ref("");
const stoppingTargetId = ref("");
let stopConfirmationTimer: number | null = null;

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
  if (citationTimer !== null) window.clearTimeout(citationTimer);
  clearStopConfirmation();
  superWorkspaceEvents?.close();
  window.removeEventListener("super-workspace:add-role-mention", handleExternalRoleMention);
  window.removeEventListener("super-workspace:chats-updated", handleChatsUpdated);
  paneToolbar.clearPaneToolbar(props.paneId);
});

watch(() => props.chatId, async () => {
  if (props.chatId === resolvedChatId.value && currentChat.value) return;
  await switchChat(props.chatId);
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
watch([() => props.paneId, () => currentChat.value?.name], registerChatToolbar, { immediate: true });

function registerChatToolbar() {
  paneToolbar.setPaneToolbar(props.paneId, {
    title: currentChat.value?.name.trim() || "Chat",
  });
}

async function loadRoles() {
  const data = await getSuperWorkspace();
  roles.value = data.roles;
}

async function switchChat(chatId: string) {
  const requestId = ++chatLoadRequest;
  beginChatSwitch(chatId);
  await nextPaint();
  if (requestId !== chatLoadRequest) return;
  await loadChatContext(chatId, requestId);
  if (requestId !== chatLoadRequest) return;
  if (resolvedChatId.value) await loadRuns(true, requestId);
  else historyLoading.value = false;
}

function beginChatSwitch(chatId: string) {
  resolvedChatId.value = chatId;
  selectedComposerMentionToken.value = "";
  currentChat.value = chatCache.value.find((chat) => chat.id === chatId) ?? null;
  items.value = [];
  nextBefore.value = null;
  runsAfterCursor.value = 0;
  hasOlderRuns.value = false;
  loadingOlder.value = false;
  historyLoading.value = Boolean(chatId);
  clearCitationPreview();
  if (threadRef.value) threadRef.value.scrollTop = 0;
}

async function nextPaint() {
  await nextTick();
  await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
}

async function loadChatContext(chatId = props.chatId, requestId = chatLoadRequest) {
  const chats = chatCache.value.length ? chatCache.value : (await listSuperChats()).chats;
  if (requestId !== chatLoadRequest) return;
  if (!chatCache.value.length) chatCache.value = chats;
  const byId = chats.find((chat) => chat.id === chatId) ?? null;
  if (byId) {
    currentChat.value = byId;
    resolvedChatId.value = byId.id;
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

function parseSuperWorkspaceChatPayload(event: Event): SuperChatCachePayload | null {
  if (!(event instanceof CustomEvent)) return null;
  const detail = event.detail as { chats?: SuperChatSummary[]; activeChatId?: string } | undefined;
  if (!detail || !Array.isArray(detail.chats)) return null;
  return {
    chats: detail.chats,
    activeChatId: typeof detail.activeChatId === "string" ? detail.activeChatId : "",
  };
}

function handleChatsUpdated(event: Event) {
  const payload = parseSuperWorkspaceChatPayload(event);
  if (payload) {
    chatCache.value = payload.chats;
  }
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

function clearCitationPreview() {
  if (citationTimer !== null) {
    window.clearTimeout(citationTimer);
    citationTimer = null;
  }
  citationPreview.value = null;
}

function citationLabel(messageId: string) {
  return `@msg-${messageId.slice(0, 8)}`;
}

function citedMessage(messageId: string) {
  const direct = items.value.find((item) => item.message_id === messageId);
  if (direct) return direct;
  const merged = displayItems.value.find((item) => item.merged_message_ids?.includes(messageId));
  return merged;
}

function uniqueCitationIds(citationIds: string[] | undefined) {
  return [...new Set(citationIds ?? [])];
}

function citationPreviewText(message: SuperDisplayItem | undefined) {
  if (!message) return "Message not loaded";
  const raw = (message.text || "").trim();
  if (!raw) return "Empty message";
  return raw.length > 280 ? `${raw.slice(0, 277)}...` : raw;
}

function jumpToMessage(messageId: string) {
  const rendered = displayItems.value.find((item) => item.message_id === messageId || item.merged_message_ids?.includes(messageId));
  const scrollTarget = rendered?.message_id;
  if (!threadRef.value || !scrollTarget) return;
  const selector = `[data-message-id="${CSS.escape(scrollTarget)}"]`;
  const target = threadRef.value.querySelector<HTMLElement>(selector);
  clearCitationPreview();
  if (!target) return;
  jumpedMessageId.value = scrollTarget;
  target.scrollIntoView({ behavior: "smooth", block: "center" });
  window.setTimeout(() => {
    if (jumpedMessageId.value === scrollTarget) jumpedMessageId.value = "";
  }, 1400);
}

function startCitationPreview(messageId: string, event: MouseEvent) {
  clearCitationPreview();
  const target = event.currentTarget as HTMLElement | null;
  const message = citedMessage(messageId);
  const title = message?.role_name || message?.role || "Message";
  const previewText = citationPreviewText(message);
  citationTimer = window.setTimeout(() => {
    if (!target) return;
    const rect = target.getBoundingClientRect();
    const width = Math.min(340, Math.max(220, Math.round(window.innerWidth * 0.35)));
    citationPreview.value = {
      messageId,
      title,
      text: previewText,
      top: rect.bottom + 8,
      left: Math.min(window.innerWidth - width - 12, Math.max(12, rect.left)),
    };
  }, 1000);
}

function stopCitationPreview() {
  clearCitationPreview();
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

function withTargetPlaceholders(orderedItems: SuperDisplayItem[]): SuperDisplayItem[] {
  const messageDriverIds = new Set(
    orderedItems
      .filter((item) => item.kind === "message" && item.driver_run_id)
      .map((item) => item.driver_run_id as string),
  );
  const expanded: SuperDisplayItem[] = [];
  for (const item of orderedItems) {
    expanded.push(item);
    if (item.kind !== "query") continue;
    for (const target of item.dispatch_targets) {
      if (!shouldShowTargetPlaceholder(target) || messageDriverIds.has(target.id)) continue;
      expanded.push(displayItemFromTargetPlaceholder(item, target));
    }
  }
  return expanded;
}

function shouldShowTargetPlaceholder(target: SuperDisplayTarget) {
  return ["queued", "claimed", "running", "failed", "cancelled"].includes(normalizedTargetStatus(target.status));
}

function displayItemFromTargetPlaceholder(query: SuperDisplayItem, target: SuperDisplayTarget): SuperDisplayItem {
  return {
    id: `${query.id}:target:${target.id}`,
    workspace_id: target.workspace_id,
    chat_id: target.chat_id,
    kind: "message",
    user_id: query.user_id,
    text: "",
    role: "assistant",
    event_type: "target:placeholder",
    provider: target.provider,
    created_at: query.created_at,
    updated_at: query.updated_at,
    message_id: `${query.message_id}:target:${target.id}`,
    query_message_id: query.message_id,
    driver_run_id: target.id,
    parent_message_id: query.parent_message_id ?? query.message_id,
    sender_role_id: query.sender_role_id ?? null,
    recipient_role_id: target.role_id,
    role_id: target.role_id,
    role_name: target.role_name,
    viewer_session_id: target.viewer_session_id,
    provider_session_id: target.provider_session_id ?? null,
    session_ref: target.session_ref,
    model_context_window: target.model_context_window ?? null,
    total_tokens: target.total_tokens ?? null,
    context_used_percent: target.context_used_percent ?? null,
    target_status: target.status,
    run_status: "",
    error: "",
    citation_ids: [],
    dispatch_targets: [],
    raw: {},
  };
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
  return item.cwd_relative ?? "";
}

function renderedItemHtml(item: SuperThreadItem) {
  return renderedItemHtmlById.value.get(item.id) ?? "";
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

function normalizedTargetStatus(status: string) {
  return (status || "").trim().toLowerCase();
}

function targetStatusLabel(status: string) {
  const normalized = normalizedTargetStatus(status);
  if (normalized === "claimed") return "starting";
  if (normalized === "queued") return "queued";
  if (normalized === "running") return "running…";
  if (normalized === "failed") return "failed";
  if (normalized === "cancelled") return "stopped";
  return normalized;
}

function targetStatusButtonLabel(item: SuperThreadItem) {
  if (stoppingTargetId.value === item.driver_run_id) return "stopping…";
  if (stopConfirmationId.value === item.driver_run_id) return "stop?";
  return targetStatusLabel(item.target_status);
}

function canStopTarget(item: SuperThreadItem) {
  return Boolean(item.driver_run_id) && normalizedTargetStatus(item.target_status) === "running";
}

function clearStopConfirmation() {
  if (stopConfirmationTimer !== null) {
    window.clearTimeout(stopConfirmationTimer);
    stopConfirmationTimer = null;
  }
  stopConfirmationId.value = "";
}

function markTargetStopped(driverRunId: string) {
  items.value = items.value.map((item) => ({
    ...item,
    target_status: item.driver_run_id === driverRunId ? "cancelled" : item.target_status,
    dispatch_targets: item.dispatch_targets.map((target) => (
      target.id === driverRunId ? { ...target, status: "cancelled" } : target
    )),
  }));
}

async function requestTargetStop(item: SuperThreadItem) {
  const driverRunId = item.driver_run_id || "";
  if (!driverRunId || stoppingTargetId.value) return;
  if (stopConfirmationId.value !== driverRunId) {
    clearStopConfirmation();
    stopConfirmationId.value = driverRunId;
    stopConfirmationTimer = window.setTimeout(clearStopConfirmation, 2000);
    return;
  }

  clearStopConfirmation();
  stoppingTargetId.value = driverRunId;
  error.value = "";
  try {
    await stopSuperWorkspaceTarget(driverRunId);
    markTargetStopped(driverRunId);
    scheduleRefreshLiveState(0);
  } catch (stopError) {
    error.value = stopError instanceof Error ? stopError.message : "Failed to stop agent";
    scheduleRefreshLiveState(0);
  } finally {
    stoppingTargetId.value = "";
  }
}

function showTargetStatus(status: string) {
  const normalized = normalizedTargetStatus(status);
  return Boolean(normalized) && normalized !== "completed";
}

function responsePlaceholderText(status: string) {
  const normalized = normalizedTargetStatus(status);
  if (normalized === "queued") return "Waiting for this role to start.";
  if (normalized === "claimed") return "Starting role session.";
  if (normalized === "failed") return "No response was produced.";
  if (normalized === "cancelled") return "Stopped before a response was produced.";
  return "Waiting for response.";
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

async function loadRuns(reset: boolean, requestId = chatLoadRequest) {
  if (!hasResolvedChatId()) return;
  const chatId = resolvedChatId.value;
  if (reset) historyLoading.value = true;
  else loadingOlder.value = true;
  const thread = reset ? null : threadRef.value;
  const previousScrollHeight = thread?.scrollHeight ?? 0;
  const previousScrollTop = thread?.scrollTop ?? 0;
  try {
    const limit = reset ? Math.min(100, Math.max(30, items.value.length || 30)) : 30;
    const page = await listSuperWorkspaceRuns(limit, reset ? undefined : nextBefore.value ?? undefined, undefined, chatId);
    if (requestId !== chatLoadRequest || chatId !== resolvedChatId.value) return;
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
    if (requestId !== chatLoadRequest) return;
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (requestId !== chatLoadRequest) return;
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
  const requestId = chatLoadRequest;
  const chatId = resolvedChatId.value;
  const after = runsAfterCursor.value;
  if (!after) {
    await loadRuns(true, requestId);
    return;
  }
  const stickToBottom = isThreadNearBottom();
  const page = await listSuperWorkspaceRuns(100, undefined, Math.max(0, after - 0.001), chatId);
  if (requestId !== chatLoadRequest || chatId !== resolvedChatId.value) return;
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
      <div
        v-if="citationPreview"
        class="super-citation-preview"
        :style="{ top: `${citationPreview.top}px`, left: `${citationPreview.left}px` }"
      >
        <div class="super-citation-preview-title">{{ citationPreview.title }}</div>
        <div class="super-citation-preview-body">{{ citationPreview.text }}</div>
      </div>
      <article
        v-for="item in displayItems"
        :key="item.id"
        class="super-run"
        :class="{ 'super-citation-highlight': jumpedMessageId === item.message_id }"
        :data-message-id="item.message_id"
      >
        <div v-if="item.kind === 'query'" class="super-user-turn">
          <div class="super-message-top super-user-message-top">
            <div class="super-message-meta">
              <span class="super-response-role-label">
                <i class="bi bi-person-fill"></i>
                User
              </span>
              <div class="super-run-time">{{ formatTime(item.created_at) }}</div>
              <div class="super-route-line">
                <template v-if="item.dispatch_targets.length">
                  <i class="bi bi-arrow-right super-route-arrow" title="Dispatched to"></i>
                  <span v-for="target in item.dispatch_targets" :key="target.id" class="super-route-chip">
                    <i class="bi" :class="targetIcon(target)"></i>
                    {{ target.role_name }}
                  </span>
                </template>
                <span v-else-if="item.run_status === 'selecting'" class="super-route-pending">selecting role to dispatch...</span>
                <span v-else-if="item.run_status === 'queued'" class="super-route-pending">queued for role dispatch...</span>
                <span v-else-if="item.run_status === 'failed'" class="super-route-error">dispatch failed: {{ item.error }}</span>
                <template v-if="item.citation_ids?.length">
                  <i class="bi bi-link-45deg super-route-arrow" title="Citations"></i>
                  <button
                    v-for="citationId in uniqueCitationIds(item.citation_ids)"
                    :key="citationId"
                    class="super-citation-chip"
                    type="button"
                    :title="`Jump to ${citationId}`"
                    @click="jumpToMessage(citationId)"
                    @mouseenter="startCitationPreview(citationId, $event)"
                    @mouseleave="stopCitationPreview"
                  >
                    <i class="bi bi-link-45deg"></i>
                    {{ citationLabel(citationId) }}
                  </button>
                </template>
              </div>
            </div>
            <button class="btn btn-sm super-cite-button" type="button" :title="`Cite @msg-${item.message_id}`" @click="addMessageCitation(item.message_id)">
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
              <button
                v-if="showTargetStatus(item.target_status) && canStopTarget(item)"
                class="super-target-status super-target-stop"
                :class="[
                  `status-${normalizedTargetStatus(item.target_status)}`,
                  { confirming: stopConfirmationId === item.driver_run_id, stopping: stoppingTargetId === item.driver_run_id },
                ]"
                type="button"
                :disabled="Boolean(stoppingTargetId)"
                :title="stopConfirmationId === item.driver_run_id ? 'Click again within 2 seconds to stop this agent' : 'Stop this running agent'"
                @click="requestTargetStop(item)"
              >
                {{ targetStatusButtonLabel(item) }}
              </button>
              <span
                v-else-if="showTargetStatus(item.target_status)"
                class="super-target-status"
                :class="`status-${normalizedTargetStatus(item.target_status)}`"
              >
                {{ targetStatusLabel(item.target_status) }}
              </span>
              <span v-if="shortSessionId(item)" class="super-meta-text super-session-id" :title="item.session_ref || item.viewer_session_id">
                {{ shortSessionId(item) }}
              </span>
              <span v-if="contextUsageLabel(item)" class="super-meta-text super-context-usage">
                {{ contextUsageLabel(item) }}
              </span>
              <span class="super-run-time">{{ formatTime(item.created_at) }}</span>
            </div>
            <button v-if="item.text.trim()" class="btn btn-sm super-cite-button" type="button" :title="`Cite @msg-${item.message_id}`" @click="addMessageCitation(item.message_id)">
              <i class="bi bi-link-45deg"></i>
              <span>Cite</span>
            </button>
          </div>
          <div class="super-role-response" @click="openItemLink($event, item)">
            <div v-if="item.text.trim()" class="markdown-body markdown-content super-response-body" v-html="renderedItemHtml(item)"></div>
            <div v-else class="super-response-placeholder">{{ responsePlaceholderText(item.target_status) }}</div>
          </div>
        </div>
      </article>
      <div v-if="!items.length && !historyLoading" class="super-empty-thread">Write one message and dispatch it into this chat.</div>
      <div v-if="historyLoading && !items.length" class="super-empty-thread">Loading history</div>
    </section>

    <div ref="composerShellRef" class="super-composer" :class="{ collapsed: composerCollapsed }" @focusout="handleComposerFocusOut">
      <button
        v-if="composerCollapsed"
        class="btn btn-outline-secondary super-composer-toggle"
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
          :explicit-navigation="true"
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
                <span class="super-dispatch-label">{{ selectedDispatchRoles.length ? selectedDispatchRoles.map((role) => role.name).join(', ') : 'Auto' }}</span>
                <span v-if="selectedDispatchRoles.length > 1" class="super-dispatch-count">{{ selectedDispatchRoles.length }}</span>
              </summary>
              <div class="list-group super-dispatch-menu" @mousedown.prevent>
                <button class="list-group-item list-group-item-action super-dispatch-menu-auto" type="button" :class="{ selected: !selectedDispatchRoles.length }" @click="dispatchSelection.clearChat(resolvedChatId)">
                  <i class="bi" :class="selectedDispatchRoles.length ? 'bi-circle' : 'bi-check-circle-fill'"></i>
                  <span>Auto</span>
                </button>
                <div v-if="!chatDispatchRoles.length" class="super-dispatch-empty">No roles in chat</div>
                <button
                  v-for="role in chatDispatchRoles"
                  :key="role.id"
                  class="list-group-item list-group-item-action super-dispatch-option"
                  type="button"
                  :class="{ selected: isDispatchRoleSelected(role.id) }"
                  @click="toggleDispatchRole(role.id)"
                >
                  <i class="bi" :class="isDispatchRoleSelected(role.id) ? 'bi-check-square-fill' : 'bi-square'"></i>
                  <span>{{ role.name }}</span>
                </button>
              </div>
            </details>
            <button class="btn voice-action-button super-send-button" type="button" :disabled="!canDispatch" title="Send message (Cmd/Ctrl+Enter)" aria-label="Send message" @click="dispatchMessage">
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
  background: var(--color-surface);
  container-type: size;
  display: flex;
  flex-direction: column;
  font-size: var(--font-size-ui);
  height: 100%;
  min-height: 0;
  position: relative;
}

.super-thread {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 10px;
  min-height: 0;
  min-width: 0;
  overflow-x: hidden;
  overflow-y: auto;
  padding: 10px 0;
}

.super-run {
  display: flex;
  flex-direction: column;
  gap: 5px;
  min-width: 0;
}

.super-user-turn,
.super-role-turn,
.super-message-meta {
  min-width: 0;
}

.super-message-top,
.super-message-meta,
.super-route-line,
.super-response-meta,
.super-composer-mentions,
.super-composer-mention {
  align-items: center;
  display: flex;
}

.super-message-top {
  color: var(--color-text-muted);
  font-size: var(--font-size-ui);
  justify-content: space-between;
  min-width: 0;
  overflow: hidden;
}

.super-route-line,
.super-composer-mentions {
  gap: 6px;
}

.super-composer-mentions {
  flex-wrap: wrap;
}

.super-message-meta,
.super-response-meta,
.super-route-line {
  flex-wrap: nowrap;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
}

.super-message-meta,
.super-response-meta {
  flex: 1 1 auto;
  gap: 6px;
}

.super-route-line {
  flex: 1 1 auto;
}

.super-run-time,
.super-route-arrow {
  flex: 0 0 auto;
}

.super-run-time {
  color: var(--color-text-muted);
  font-size: 11px;
  line-height: 1.2;
}

.super-user-message,
.super-role-response {
  background: var(--color-surface-muted);
  border: 0;
  border-radius: var(--radius-md);
  min-width: 0;
  overflow-x: hidden;
  padding: 8px 10px;
  user-select: text;
  width: 100%;
}

.super-user-message {
  background: var(--color-surface-muted);
  max-width: none;
}

.super-role-response {
  background: var(--color-surface-muted);
  overflow-x: auto;
  overflow-y: visible;
}

.super-message-text {
  overflow-wrap: anywhere;
  user-select: text;
  white-space: pre-wrap;
  word-break: break-word;
}

.super-response-body {
  --markdown-render-body-size: var(--font-size-ui);
  --markdown-render-h1-size: 20px;
  --markdown-render-h2-size: 17px;
  --markdown-render-h3-size: 15px;
  --markdown-render-h4-size: 14px;
  --markdown-render-paragraph-line-height: 1.48;
  --markdown-render-paragraph-size: var(--font-size-ui);
  --markdown-render-pre-padding: 8px;
  max-width: 100%;
  min-width: 0;
  overflow-wrap: anywhere;
  user-select: text;
  word-break: break-word;
}

.super-response-body :deep(*) {
  max-width: 100%;
  user-select: text;
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
  border: 1px solid var(--color-border);
  padding: 6px 8px;
}

.super-response-body :deep(.markdown-code-line),
.super-response-body :deep(.markdown-code-line-content) {
  min-width: 0;
}

.super-response-role-label {
  color: var(--color-text);
  display: inline-flex;
  gap: 5px;
  align-items: center;
  font-size: var(--font-size-ui);
  font-weight: 700;
}

.super-meta-text {
  color: var(--color-text-muted);
  font-size: 11px;
  line-height: 1.2;
  overflow: hidden;
  text-overflow: ellipsis;
}

.super-target-status {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  font-size: 11px;
  font-weight: 600;
  line-height: 1.2;
  padding: 2px 7px;
}

.super-target-status.status-running,
.super-target-status.status-claimed {
  background: color-mix(in srgb, var(--color-warning) 13%, var(--color-surface));
  border-color: color-mix(in srgb, var(--color-warning) 42%, var(--color-border));
  color: var(--color-warning);
}

.super-target-status.status-queued {
  background: var(--color-accent-soft);
  border-color: color-mix(in srgb, var(--color-accent) 35%, var(--color-border));
  color: var(--color-accent-hover);
}

.super-target-status.status-failed {
  background: color-mix(in srgb, var(--color-danger) 12%, var(--color-surface));
  border-color: color-mix(in srgb, var(--color-danger) 40%, var(--color-border));
  color: var(--color-danger);
}

.super-target-status.status-cancelled {
  background: var(--color-surface-raised);
  border-color: var(--color-border);
  color: var(--color-text-muted);
}

.super-target-stop {
  cursor: pointer;
  font-family: inherit;
}

.super-target-stop:hover:not(:disabled) {
  background: color-mix(in srgb, var(--color-warning) 19%, var(--color-surface));
  color: var(--color-warning);
}

.super-target-stop.confirming {
  background: color-mix(in srgb, var(--color-danger) 13%, var(--color-surface));
  border-color: color-mix(in srgb, var(--color-danger) 42%, var(--color-border));
  color: var(--color-danger);
}

.super-target-stop.stopping,
.super-target-stop:disabled {
  cursor: default;
  opacity: 0.72;
}

.super-response-placeholder {
  color: var(--color-text-muted);
  font-size: var(--font-size-ui);
  font-style: italic;
  line-height: 1.45;
}

.super-cite-button,
.super-route-chip,
.super-citation-chip,
.super-composer-mention {
  border-radius: var(--radius-sm);
}

.super-cite-button {
  background: transparent;
  border: 0;
  color: var(--color-text-muted);
  display: inline-flex;
  flex: 0 0 auto;
  font-size: var(--font-size-ui-small);
  gap: 4px;
  height: 24px;
  line-height: 1;
  padding: 0 6px;
}

.super-cite-button:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.super-dispatch-picker {
  align-items: center;
  display: inline-block;
  flex: 0 0 auto;
  height: 32px;
  margin: 0;
  padding: 0;
  position: relative;
  max-width: min(220px, 34vw);
  width: auto;
}

.super-dispatch-picker summary {
  align-items: center;
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  cursor: pointer;
  display: inline-flex;
  height: 32px;
  gap: 5px;
  justify-content: center;
  list-style: none;
  position: relative;
  max-width: min(220px, 34vw);
  min-width: 58px;
  padding: 0 8px;
}

.super-dispatch-picker summary::-webkit-details-marker {
  display: none;
}

.super-dispatch-picker.active {
  color: var(--color-text-muted);
}

.super-dispatch-picker.active summary {
  background: transparent;
  border-color: var(--color-border);
  color: var(--color-text-muted);
}

.super-dispatch-picker .bi {
  font-size: 14px;
  line-height: 1;
  pointer-events: none;
}

.super-dispatch-count {
  align-items: center;
  background: transparent;
  border-radius: 999px;
  color: var(--color-text-muted);
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

.super-dispatch-label {
  font-size: 11px;
  font-weight: 700;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.super-dispatch-menu {
  --bs-list-group-action-active-bg: var(--color-surface-hover);
  --bs-list-group-action-active-color: var(--color-text);
  --bs-list-group-action-hover-bg: var(--color-surface-hover);
  --bs-list-group-action-hover-color: var(--color-text);
  --bs-list-group-bg: transparent;
  --bs-list-group-border-radius: 0;
  --bs-list-group-border-width: 0;
  --bs-list-group-color: var(--color-text);
  background: var(--color-surface-raised);
  border: 0;
  border-radius: var(--radius-md);
  bottom: calc(100% + 6px);
  display: flex;
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
  display: flex;
  font-size: 12px;
  gap: 7px;
  min-width: 0;
  padding: 6px 7px;
  text-align: left;
  width: 100%;
}

.super-dispatch-menu-auto.selected {
  color: var(--color-accent-hover);
  font-weight: 700;
}

.super-dispatch-option.selected {
  color: var(--color-accent-hover);
  font-weight: 700;
}

.super-dispatch-menu-auto:hover,
.super-dispatch-menu-auto:focus,
.super-dispatch-option:hover,
.super-dispatch-option:focus {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.super-dispatch-menu-auto.selected:hover,
.super-dispatch-menu-auto.selected:focus,
.super-dispatch-option.selected:hover,
.super-dispatch-option.selected:focus {
  color: var(--color-accent-hover);
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
  color: var(--color-text-muted);
  font-size: 12px;
  padding: 7px;
}

.super-route-chip {
  background: var(--color-accent-soft);
  color: var(--color-accent-hover);
  display: inline-flex;
  flex: 0 0 auto;
  font: inherit;
  gap: 4px;
  line-height: 1.2;
  padding: 2px 6px;
}

.super-citation-chip {
  background: var(--color-reference-soft);
  border: 0;
  color: var(--color-reference);
  display: inline-flex;
  flex: 0 0 auto;
  font: inherit;
  gap: 4px;
  line-height: 1.2;
  padding: 2px 6px;
}

.super-citation-chip,
.super-route-chip,
.super-cite-button {
  align-items: center;
}

.super-citation-preview {
  background: var(--color-surface-raised);
  border: 0;
  border-radius: var(--radius-md);
  color: var(--color-text);
  left: 0;
  max-width: 340px;
  min-width: 220px;
  pointer-events: none;
  padding: 8px 10px;
  position: fixed;
  right: auto;
  top: 0;
  z-index: 30;
}

.super-citation-preview-title {
  font-size: 11px;
  font-weight: 600;
  margin-bottom: 4px;
  opacity: 0.88;
}

.super-citation-preview-body {
  font-size: 11.5px;
  line-height: 1.35;
  opacity: 0.93;
  white-space: pre-wrap;
  word-break: break-word;
}

.super-citation-highlight {
  outline: 2px solid var(--color-focus);
  outline-offset: 2px;
}

.super-route-pending {
  color: var(--color-warning);
}

.super-route-error,
.super-error {
  color: var(--color-danger);
}

.super-composer {
  flex: 0 0 auto;
  min-width: 0;
  padding: 0;
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
  background: var(--color-surface-muted);
  border: 0;
  border-radius: var(--radius-md);
  padding: 3px;
  width: 100%;
}

.super-composer.collapsed .super-composer-card {
  display: none;
}

.super-composer-toggle {
  align-items: center;
  background: var(--color-surface-raised);
  border: 0;
  border-radius: var(--radius-md);
  color: var(--color-accent-hover);
  display: inline-flex;
  height: 42px;
  justify-content: center;
  padding: 0;
  width: 42px;
}

.super-composer-toggle:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.super-composer-toggle .bi {
  font-size: 18px;
  line-height: 1;
}

.super-pin-button.active {
  background: var(--color-surface-selected);
  border-color: var(--color-border);
  color: var(--color-text);
}

.super-send-button {
  background: var(--color-surface-selected);
  border: 0;
  color: var(--color-text);
}

.super-send-button:hover:not(:disabled) {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.super-send-button:disabled {
  background: transparent;
  color: var(--color-text-subtle);
}

.super-composer-mentions {
  margin-bottom: 8px;
}

.super-composer-mention {
  background: var(--color-surface-muted);
  color: var(--color-text);
  gap: 5px;
  padding: 3px 7px;
}

.super-composer-mention.selected {
  background: color-mix(in srgb, var(--color-danger) 10%, var(--color-surface));
  border-color: color-mix(in srgb, var(--color-danger) 45%, var(--color-border));
}

.super-composer-mention-token {
  color: var(--color-text-muted);
  font-size: 11px;
}

.super-empty-thread,
.super-history-boundary {
  color: var(--color-text-muted);
  padding: 24px;
  text-align: center;
}

@media (max-width: 767.98px) {
  .super-thread {
    gap: 8px;
    padding: 6px 0;
  }

  .super-run {
    gap: 4px;
  }

  .super-message-top {
    font-size: 10.5px;
    gap: 6px;
  }

  .super-response-body {
    --markdown-render-body-size: var(--font-size-ui);
    --markdown-render-h1-size: 17px;
    --markdown-render-h2-size: 15px;
    --markdown-render-h3-size: 13.5px;
    --markdown-render-h4-size: 13px;
    --markdown-render-paragraph-line-height: 1.42;
    --markdown-render-paragraph-size: var(--font-size-ui);
    --markdown-render-pre-padding: 6px;
  }

  .super-route-line,
  .super-composer-mentions {
    gap: 4px;
  }

  .super-user-message,
  .super-role-response {
    border-radius: var(--radius-sm);
    font-size: var(--font-size-ui);
    line-height: 1.45;
    padding: 7px 8px;
  }

  .super-response-role-label {
    font-size: var(--font-size-ui);
  }

  .super-cite-button {
    padding: 0 5px;
  }

  .super-route-chip {
    padding: 2px 5px;
  }

  .super-composer {
    padding: 0;
  }

  .super-composer.collapsed {
    bottom: max(6px, env(safe-area-inset-bottom));
    right: 6px;
  }

  .super-composer-card {
    border-radius: var(--radius-sm);
    padding: 6px;
  }

  .super-empty-thread,
  .super-history-boundary {
    font-size: 12px;
    padding: 12px 8px;
  }
}

@container (max-width: 560px) {
  .super-session-id {
    display: none;
  }
}

@container (max-width: 440px) {
  .super-context-usage {
    display: none;
  }
}

@container (max-width: 340px) {
  .super-message-meta .super-route-line {
    display: none;
  }
}
</style>
