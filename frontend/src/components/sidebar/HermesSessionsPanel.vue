<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useFilesStore } from "../../stores/files";
import { useHermesStore } from "../../stores/hermes";
import { useLayoutStore } from "../../stores/layout";
import { useVoiceStore } from "../../stores/voice";
import { useWorkspacesStore } from "../../stores/workspaces";
import type { HermesSessionInfo } from "../../types/hermes";

const emit = defineEmits<{
  "open-hermes-session": [id: string];
}>();

const props = defineProps<{
  sessionIds: string[];
}>();

const hermes = useHermesStore();
const files = useFilesStore();
const layout = useLayoutStore();
const voice = useVoiceStore();
const workspaces = useWorkspacesStore();
const hermesError = ref("");
const visibleSessions = computed(() => {
  const ids = new Set(props.sessionIds);
  return hermes.sessions.filter((session) => ids.has(session.id));
});

function voiceContextId(sessionId: string) {
  return `hermes:${sessionId}:prompt`;
}

function hasVoicePending(sessionId: string) {
  const status = voice.context(voiceContextId(sessionId)).status;
  return status === "connecting" || status === "recording" || status === "processing";
}

function hasVoiceReady(sessionId: string) {
  return voice.hasReadyText(voiceContextId(sessionId));
}

function sessionStatusIndicator(session: HermesSessionInfo) {
  if (session.status === "failed") return "failed";
  if (session.status === "exited" && hermes.unreadSessionIds.includes(session.id)) return "completed";
  if (session.status === "running") return "running";
  return "";
}

function sessionStatusTitle(session: HermesSessionInfo) {
  const indicator = sessionStatusIndicator(session);
  if (indicator === "failed") return "Hermes run failed";
  if (indicator === "completed") return "Hermes run finished";
  if (indicator === "running") return "Hermes run is running";
  return "";
}

watch(
  visibleSessions,
  (sessions) => {
    for (const session of sessions) {
      if (session.status === "exited") hermes.markRead(session.id);
    }
  },
  { immediate: true },
);

async function newHermesSession() {
  hermesError.value = "";
  try {
    const session = await hermes.create("", files.currentPath);
    workspaces.rememberActiveHermesSession(session.id);
    emit("open-hermes-session", session.id);
  } catch (err) {
    hermesError.value = err instanceof Error ? err.message : String(err);
  }
}

function closeHermesSession(id: string) {
  workspaces.forgetActiveHermesSession(id);
  layout.clearHermesSession(id);
}
</script>

<template>
  <div class="sidebar-panel">
    <div class="sidebar-section">
      <button class="btn btn-sm btn-primary panel-command" type="button" @click="newHermesSession">
        <i class="bi bi-lightning-charge"></i>
        <span>New Hermes</span>
      </button>
      <div v-if="hermesError" class="hermes-error">{{ hermesError }}</div>
    </div>

    <div class="sidebar-section list-section">
      <div class="section-title">Hermes</div>
      <div v-if="!visibleSessions.length" class="empty-panel">No Hermes sessions in this workspace</div>
      <div
        v-for="session in visibleSessions"
        :key="session.id"
        class="sidebar-row"
        :class="[
          {
            active: layout.openHermesSessionIds.includes(session.id),
            'voice-pending': hasVoicePending(session.id),
            'voice-ready': hasVoiceReady(session.id),
          },
        ]"
      >
        <span
          class="session-status-dot"
          :class="sessionStatusIndicator(session) ? `status-${sessionStatusIndicator(session)}` : ''"
          :title="sessionStatusTitle(session)"
          :aria-label="sessionStatusTitle(session)"
        ></span>
        <button class="sidebar-row-main" type="button" @click="emit('open-hermes-session', session.id)">
          <span class="sidebar-row-name">{{ session.title }}</span>
        </button>
        <button class="btn btn-sm icon-button sidebar-row-action" type="button" title="Remove from workspace" @click="closeHermesSession(session.id)">
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

.panel-command {
  align-items: center;
  display: inline-flex;
  gap: 7px;
  justify-content: center;
  width: 100%;
}

.hermes-error {
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

.sidebar-row.voice-ready {
  background: #fff0b8;
}

.sidebar-row-main {
  align-items: center;
  background: transparent;
  border: 0;
  color: inherit;
  display: flex;
  flex: 1 1 auto;
  min-width: 0;
  padding: 0;
  text-align: left;
}

.sidebar-row-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sidebar-row-action {
  flex: 0 0 auto;
  height: 23px;
  opacity: 0;
  padding: 0;
  width: 23px;
}

.sidebar-row:hover .sidebar-row-action {
  opacity: 1;
}

.session-status-dot {
  border-radius: 999px;
  flex: 0 0 8px;
  height: 8px;
  width: 8px;
}

.session-status-dot.status-running {
  background: #1f8f4d;
}

.session-status-dot.status-completed {
  background: #2f6fdd;
}

.session-status-dot.status-failed {
  background: #bf2f2f;
}
</style>
