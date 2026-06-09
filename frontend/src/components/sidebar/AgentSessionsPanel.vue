<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useFilesStore } from "../../stores/files";
import { useAgentsStore } from "../../stores/agents";
import { useLayoutStore } from "../../stores/layout";
import { useVoiceStore } from "../../stores/voice";
import { useWorkspacesStore } from "../../stores/workspaces";
import { sortAgentSessions } from "../../stores/agents";
import type { AgentProvider, AgentSessionInfo } from "../../types/agents";
import { parseAgentRef } from "../../utils/agents";

const emit = defineEmits<{
  "open-agent-session": [ref: string];
}>();

const props = defineProps<{ sessionRefs: string[] }>();

const agents = useAgentsStore();
const files = useFilesStore();
const layout = useLayoutStore();
const voice = useVoiceStore();
const workspaces = useWorkspacesStore();
const error = ref("");
const selectedProvider = ref<AgentProvider>("");
const visibleSessions = computed(() => {
  const workspaceRefs = new Set(props.sessionRefs);
  return sortAgentSessions(agents.sessions.filter((session) => workspaceRefs.has(session.ref)));
});

const selectedProviderInfo = computed(() => {
  const provider = selectedProvider.value || agents.providers[0]?.id || "";
  return agents.providerById(provider);
});

watch(
  () => agents.providers,
  (providers) => {
    if (!selectedProvider.value || !providers.some((provider) => provider.id === selectedProvider.value)) {
      selectedProvider.value = providers[0]?.id ?? "";
    }
  },
  { immediate: true },
);

function voiceContextId(session: AgentSessionInfo) {
  return `${session.provider}:${session.id}:prompt`;
}

function hasVoicePending(session: AgentSessionInfo) {
  const status = voice.context(voiceContextId(session)).status;
  return status === "connecting" || status === "recording" || status === "processing";
}

function hasVoiceReady(session: AgentSessionInfo) {
  return voice.hasReadyText(voiceContextId(session));
}

function sessionStatusIndicator(session: AgentSessionInfo) {
  if (session.status === "failed" && agents.unreadSessionRefs.includes(session.ref)) return "failed";
  if (session.status === "exited" && agents.unreadSessionRefs.includes(session.ref)) return "completed";
  if (session.status === "running") return "running";
  return "";
}

function sessionStatusTitle(session: AgentSessionInfo) {
  const indicator = sessionStatusIndicator(session);
  const provider = agents.providerById(session.provider).name;
  if (indicator === "failed") return `${provider} run failed`;
  if (indicator === "completed") return `${provider} run finished`;
  if (indicator === "running") return `${provider} run is running`;
  return "";
}

watch(
  visibleSessions,
  (sessions) => {
    for (const session of sessions) {
      if (session.status === "exited") agents.markRead(session.ref);
    }
  },
  { immediate: true },
);

async function newSession() {
  error.value = "";
  const provider = selectedProvider.value || agents.providers[0]?.id;
  if (!provider) return;
  try {
    const session = await agents.create(provider, "", files.currentPath);
    await workspaces.rememberActiveAgentSession(session.ref);
    emit("open-agent-session", session.ref);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function closeSession(ref: string) {
  await workspaces.forgetActiveAgentSession(ref);
  layout.clearAgentSession(ref);
}

function sessionIcon(session: AgentSessionInfo) {
  return agents.providerById(session.provider).icon;
}

function sessionProviderName(session: AgentSessionInfo) {
  return agents.providerById(session.provider).name;
}

function isOpen(ref: string) {
  return layout.openAgentSessionRefs.includes(ref);
}

function ensureValidRef(ref: string) {
  return parseAgentRef(ref) !== null;
}
</script>

<template>
  <div class="sidebar-panel">
    <div class="sidebar-section">
      <div class="agent-new-row">
        <select v-model="selectedProvider" class="form-select form-select-sm" aria-label="Agent provider">
          <option v-for="provider in agents.providers" :key="provider.id" :value="provider.id">{{ provider.name }}</option>
        </select>
        <button class="btn btn-sm btn-primary panel-command" type="button" @click="newSession">
          <i class="bi" :class="selectedProviderInfo.icon"></i>
          <span>New</span>
        </button>
      </div>
      <div v-if="error" class="agent-error">{{ error }}</div>
    </div>

    <div class="sidebar-section list-section">
      <div class="section-title">Agents</div>
      <div v-if="!visibleSessions.length" class="empty-panel">No agent sessions in this workspace</div>
      <div
        v-for="session in visibleSessions"
        :key="session.ref"
        class="sidebar-row"
        :class="[
          {
            active: isOpen(session.ref),
            'voice-pending': hasVoicePending(session),
            'voice-ready': hasVoiceReady(session),
          },
        ]"
      >
        <span
          class="session-status-dot"
          :class="sessionStatusIndicator(session) ? `status-${sessionStatusIndicator(session)}` : ''"
          :title="sessionStatusTitle(session)"
          :aria-label="sessionStatusTitle(session)"
        ></span>
        <i class="bi session-provider-icon" :class="sessionIcon(session)" :title="sessionProviderName(session)" :aria-label="sessionProviderName(session)"></i>
        <button class="sidebar-row-main" type="button" :disabled="!ensureValidRef(session.ref)" @click="emit('open-agent-session', session.ref)">
          <span class="sidebar-row-name">{{ session.title }}</span>
        </button>
        <button
          class="btn btn-sm icon-button sidebar-row-action"
          type="button"
          title="Remove from workspace"
          aria-label="Remove from workspace"
          @click="closeSession(session.ref)"
        >
          <i class="bi bi-x"></i>
        </button>
      </div>
    </div>
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

.section-title {
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0;
  margin-bottom: 6px;
  text-transform: uppercase;
}

.agent-new-row {
  display: grid;
  gap: 6px;
  grid-template-columns: minmax(0, 1fr) auto;
}

.panel-command {
  align-items: center;
  display: inline-flex;
  gap: 7px;
  justify-content: center;
  white-space: nowrap;
}

.agent-error {
  color: #a33;
  font-size: 12px;
  margin-top: 7px;
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
  box-sizing: border-box;
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

.sidebar-row.voice-pending {
  background: #fff6d7;
}

.sidebar-row.voice-pending:hover {
  background: #ffedb0;
}

.sidebar-row.voice-ready {
  background: #fff0b8;
}

.sidebar-row.voice-ready:hover {
  background: #ffe48a;
}

.sidebar-row-main {
  align-items: center;
  background: transparent;
  border: 0;
  color: inherit;
  display: flex;
  flex: 1 1 auto;
  gap: 7px;
  min-width: 0;
  padding: 0;
  text-align: left;
}

.sidebar-row-name {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-provider-icon {
  color: #5f6f86;
  flex: 0 0 auto;
  font-size: 13px;
  width: 14px;
}

.session-status-dot {
  border-radius: 999px;
  flex: 0 0 auto;
  height: 8px;
  opacity: 0;
  width: 8px;
}

.session-status-dot.status-running {
  background: #2da44e;
  opacity: 1;
}

.session-status-dot.status-completed {
  background: #f0ad00;
  opacity: 1;
}

.session-status-dot.status-failed {
  background: #d1242f;
  opacity: 1;
}

.sidebar-row-action {
  flex: 0 0 auto;
  height: 24px;
  opacity: 0.75;
  width: 24px;
}

.star-button .bi-star-fill {
  color: #d89b00;
}
</style>
