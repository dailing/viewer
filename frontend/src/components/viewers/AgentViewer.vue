<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { resolveDirectoryLink } from "../../api/client";
import { useAgentsStore } from "../../stores/agents";
import { useCodexStore } from "../../stores/codex";
import { useFilesStore } from "../../stores/files";
import { useLayoutStore } from "../../stores/layout";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { useVoiceStore } from "../../stores/voice";
import type { PaneToolbarAction, PaneToolbarControl } from "../../stores/paneToolbar";
import type { AgentEvent, AgentSessionInfo, AgentSessionSnapshot, AgentSocketMessage } from "../../types/agents";
import { agentSessionSocketUrl } from "../../api/client";
import { agentRef, parseAgentRef, toAgentSessionInfo, toAgentSessionSnapshot } from "../../utils/agents";
import { namespacedStorageKey } from "../../utils/userProfile";
import AgentPromptComposer from "../AgentPromptComposer.vue";
import AgentSessionTranscript from "../AgentSessionTranscript.vue";
import LocalFilePreview from "../LocalFilePreview.vue";

const props = defineProps<{ agentRef: string; paneId: string }>();
const agents = useAgentsStore();
const codex = useCodexStore();
const files = useFilesStore();
const layout = useLayoutStore();
const paneToolbar = usePaneToolbarStore();
const voice = useVoiceStore();
const session = ref<AgentSessionSnapshot | null>(null);
const promptText = ref(loadPromptDraft(props.agentRef));
const error = ref("");
const transcript = ref<InstanceType<typeof AgentSessionTranscript> | null>(null);
const focusMode = ref(true);
const rawJson = ref(false);
const stopping = ref(false);
const creatingSession = ref(false);
const previewPath = ref<string | null>(null);

const AGENT_DRAFTS_KEY = "viewer.agentDrafts.v1";

let socket: WebSocket | null = null;
let reconnectTimer: number | null = null;
let mounted = false;

const parsedRef = computed(() => parseAgentRef(props.agentRef));
const provider = computed(() => parsedRef.value?.provider ?? "");
const providerSessionId = computed(() => parsedRef.value?.id ?? "");
const providerInfo = computed(() => agents.providerById(provider.value));
const isCodex = computed(() => provider.value === "codex");
const isActivePane = computed(() => layout.activePaneId === props.paneId);
const voiceContextId = computed(() => `${provider.value}:${providerSessionId.value}:prompt`);
const codexStatusItems = computed(() => {
  if (!isCodex.value || !session.value) return [];
  const items: string[] = [];
  const raw = session.value.raw;
  const contextUsed = raw.context_used_percent;
  if (typeof contextUsed === "number") items.push(`ctx ${contextUsed}%`);
  if (typeof codex.status.primary_remaining_percent === "number" && codex.status.primary_window_minutes) {
    const hours = Math.round(codex.status.primary_window_minutes / 60);
    items.push(`${codex.status.primary_remaining_percent}%/${hours}h left`);
  }
  if (typeof codex.status.secondary_remaining_percent === "number" && codex.status.secondary_window_minutes) {
    const days = Math.round(codex.status.secondary_window_minutes / (60 * 24));
    items.push(`${codex.status.secondary_remaining_percent}%/${days}d left`);
  }
  return items;
});
const selectedModel = computed(() => (isCodex.value ? codex.models.selected_model : undefined));
const requestedDetail = computed<"focus" | "full">(() => (focusMode.value && !rawJson.value ? "focus" : "full"));

function readPromptDrafts(): Record<string, string> {
  try {
    const raw = localStorage.getItem(namespacedStorageKey(AGENT_DRAFTS_KEY));
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? (parsed as Record<string, string>) : {};
  } catch {
    return {};
  }
}

function writePromptDrafts(drafts: Record<string, string>) {
  try {
    const key = namespacedStorageKey(AGENT_DRAFTS_KEY);
    if (Object.keys(drafts).length) localStorage.setItem(key, JSON.stringify(drafts));
    else localStorage.removeItem(key);
  } catch {
    // Draft persistence is best-effort.
  }
}

function loadPromptDraft(ref: string): string {
  const draft = readPromptDrafts()[ref];
  return typeof draft === "string" ? draft : "";
}

function savePromptDraft(ref: string, text: string) {
  const drafts = readPromptDrafts();
  if (text) drafts[ref] = text;
  else delete drafts[ref];
  writePromptDrafts(drafts);
}

function clearPromptDraft(ref: string) {
  savePromptDraft(ref, "");
}

function restorePromptDraft() {
  promptText.value = loadPromptDraft(props.agentRef);
}

function syncProviderStore(info: AgentSessionInfo) {
  if (info.provider === "codex") codex.upsert(info.raw as any);
}

async function openAgentLink(target: string) {
  error.value = "";
  try {
    const resolved = await resolveDirectoryLink(session.value?.cwd_relative ?? "", target);
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

function openPreviewInSplit(path: string, direction: "horizontal" | "vertical") {
  previewPath.value = null;
  void files.recordVisit(path);
  layout.openFileInSplit(path, direction);
}

function applyInfo(info: AgentSessionInfo) {
  agents.upsert(info);
  syncProviderStore(info);
  if (!session.value) return;
  session.value.provider_session_id = info.provider_session_id;
  session.value.title = info.title;
  session.value.cwd = info.cwd;
  session.value.cwd_relative = info.cwd_relative;
  session.value.status = info.status;
  session.value.exit_code = info.exit_code;
  session.value.event_count = info.event_count;
  session.value.updated_at = info.updated_at;
  session.value.model = info.model;
  session.value.total_tokens = info.total_tokens;
  session.value.queue = info.queue ?? [];
  session.value.pending_approvals = info.pending_approvals ?? [];
  session.value.raw = info.raw;
  if (isActivePane.value) agents.markRead(props.agentRef);
}

function applySnapshot(snapshot: AgentSessionSnapshot) {
  session.value = snapshot;
  agents.upsert(snapshot);
  syncProviderStore(snapshot);
  updatePaneToolbar();
  void transcript.value?.renderRichContent();
  transcript.value?.scrollToBottom(true);
}

function applyEvent(event: AgentEvent, info: AgentSessionInfo) {
  if (!session.value) return;
  if (!session.value.events.some((item) => item.index === event.index)) session.value.events.push(event);
  applyInfo(info);
  updatePaneToolbar();
  void transcript.value?.renderRichContent();
  transcript.value?.scrollToBottom(false);
  if (isActivePane.value) agents.markRead(props.agentRef);
  else agents.markUnread(props.agentRef);
}

async function loadSnapshot(detail: "focus" | "full" = requestedDetail.value) {
  const parsed = parsedRef.value;
  if (!parsed) {
    error.value = "Invalid agent session reference.";
    return;
  }
  try {
    error.value = "";
    applySnapshot(await agents.snapshot(props.agentRef, detail));
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function connect() {
  const parsed = parsedRef.value;
  if (!parsed) return;
  if (reconnectTimer !== null) {
    window.clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  socket?.close();
  const activeSocket = new WebSocket(agentSessionSocketUrl(parsed.provider, parsed.id, requestedDetail.value));
  socket = activeSocket;
  activeSocket.addEventListener("message", (event) => {
    if (socket !== activeSocket) return;
    const message = JSON.parse(event.data) as AgentSocketMessage;
    if (message.type === "snapshot") applySnapshot(toAgentSessionSnapshot(message.session as any, parsed.provider));
    if (message.type === "event") applyEvent(message.event, toAgentSessionInfo(message.session as any, parsed.provider));
    if (message.type === "status") applyInfo(toAgentSessionInfo(message.session as any, parsed.provider));
    if (message.type === "deleted") error.value = `${providerInfo.value.name} session was deleted.`;
    updatePaneToolbar();
  });
  activeSocket.addEventListener("close", () => {
    if (socket !== activeSocket) return;
    socket = null;
    if (!mounted) return;
    if (!session.value) void loadSnapshot();
    reconnectTimer = window.setTimeout(connect, 1200);
  });
  activeSocket.addEventListener("error", () => {
    if (socket !== activeSocket) return;
    error.value = `${providerInfo.value.name} session connection failed.`;
    activeSocket.close();
  });
}

function handleRefresh(event: Event) {
  const paneId = (event as CustomEvent<{ paneId?: string }>).detail?.paneId;
  if (paneId !== props.paneId) return;
  session.value = null;
  error.value = "";
  connect();
}

async function queuePrompt() {
  const prompt = promptText.value.trim();
  if (!prompt || !session.value) return;
  error.value = "";
  try {
    applyInfo(await agents.queue(props.agentRef, prompt, selectedModel.value));
    clearPromptDraft(props.agentRef);
    promptText.value = "";
    voice.clear(voiceContextId.value);
    transcript.value?.scrollToBottom(true);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function clearPrompt() {
  clearPromptDraft(props.agentRef);
  promptText.value = "";
  voice.clear(voiceContextId.value);
}

async function stopRun() {
  if (!session.value || session.value.status !== "running" || stopping.value) return;
  stopping.value = true;
  error.value = "";
  try {
    applyInfo(await agents.terminate(props.agentRef));
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    stopping.value = false;
  }
}

async function resolveApproval(payload: { approvalId: string; choice: string; all?: boolean }) {
  if (!session.value) return;
  error.value = "";
  try {
    applyInfo(await agents.resolveApproval(props.agentRef, payload.approvalId, payload.choice, payload.all ?? false));
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function createSessionHere() {
  if (!session.value || creatingSession.value || !provider.value) return;
  creatingSession.value = true;
  error.value = "";
  try {
    const nextSession = await agents.create(provider.value, "", session.value.cwd, selectedModel.value);
    layout.openAgentSession(nextSession.ref);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    creatingSession.value = false;
  }
}

function updatePaneToolbar() {
  const status = session.value?.status ?? "connecting";
  const actions: PaneToolbarAction[] = [
    { id: "agent-new-session-here", title: `New ${providerInfo.value.name} session in this directory`, icon: "bi-plus-square", run: () => createSessionHere() },
    {
      id: "agent-focus",
      title: focusMode.value ? "Show agent operation details" : "Focus mode: hide agent operation details",
      icon: focusMode.value ? "bi-eye-slash" : "bi-eye",
      active: focusMode.value,
      run: () => {
        focusMode.value = !focusMode.value;
        updatePaneToolbar();
      },
    },
    { id: "agent-raw", title: "Toggle raw JSON preview", label: "JSON", active: rawJson.value, run: () => { rawJson.value = !rawJson.value; updatePaneToolbar(); } },
  ];
  const controls: PaneToolbarControl[] = [];
  if (isCodex.value) {
    controls.push({
      kind: "select",
      id: "agent-codex-model",
      title: "Model for new Codex turns",
      value: codex.models.selected_model,
      options: codex.models.available_models,
      size: "compact",
      onChange: (value) => codex.setSelectedModel(value),
    });
  }
  const chips = isCodex.value ? codexStatusItems.value : session.value?.total_tokens ? [`${session.value.total_tokens} tokens`] : [];
  if (chips.length) controls.push({ kind: "chips", id: "agent-status", title: String(session.value?.raw?.rollout_path ?? session.value?.raw?.db_path ?? ""), items: chips });
  paneToolbar.setPaneToolbar(props.paneId, {
    title: session.value?.title ?? providerInfo.value.name,
    status: status === "running" ? `${providerInfo.value.name} running` : status,
    statusClass: status === "failed" ? "status-danger" : status === "running" ? "status-running" : status,
    actions,
    controls,
  });
}

watch(() => props.agentRef, (ref, previousRef) => {
  if (previousRef) savePromptDraft(previousRef, promptText.value);
  socket?.close();
  promptText.value = loadPromptDraft(ref);
  session.value = null;
  connect();
});
watch(promptText, (text) => {
  savePromptDraft(props.agentRef, text);
});
watch(focusMode, updatePaneToolbar);
watch(rawJson, updatePaneToolbar);
watch(requestedDetail, () => {
  if (!mounted) return;
  session.value = null;
  connect();
});
watch(() => [codex.models.selected_model, codex.models.available_models, codex.status], updatePaneToolbar, { deep: true });
watch(isActivePane, (active) => {
  if (active) agents.markRead(props.agentRef);
});

onMounted(() => {
  mounted = true;
  if (isActivePane.value) agents.markRead(props.agentRef);
  if (!agents.providersLoaded) void agents.loadProviders();
  connect();
  window.addEventListener("viewer:pane-refresh", handleRefresh);
  updatePaneToolbar();
});

onUnmounted(() => {
  mounted = false;
  if (reconnectTimer !== null) window.clearTimeout(reconnectTimer);
  window.removeEventListener("viewer:pane-refresh", handleRefresh);
  socket?.close();
  paneToolbar.clearPaneToolbar(props.paneId);
});
</script>

<template>
  <div class="agent-viewer">
    <AgentSessionTranscript
      ref="transcript"
      :session="session"
      :provider-name="providerInfo.name"
      :provider-session-id="session?.provider_session_id"
      :idle-icon="providerInfo.icon"
      :idle-text="`Send a message to start this ${providerInfo.name} session.`"
      :running-text="`${providerInfo.name} is running`"
      :error="error"
      :focus-mode="focusMode"
      :show-raw-json="rawJson"
      :muted-alpha="files.codexConfig.muted_message_alpha"
      @open-link="openAgentLink"
      @resolve-approval="resolveApproval"
    />
    <AgentPromptComposer
      v-model="promptText"
      :voice-context-id="voiceContextId"
      :status="session?.status"
      :stopping="stopping"
      :placeholder="`Send a message to this ${providerInfo.name} session`"
      @queue-prompt="queuePrompt"
      @clear-prompt="clearPrompt"
      @stop-run="stopRun"
    />
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
.agent-viewer {
  background: #f7f8fb;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  user-select: text;
}
</style>
