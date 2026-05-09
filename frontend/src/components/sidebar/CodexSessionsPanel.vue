<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useCodexStore } from "../../stores/codex";
import { useFilesStore } from "../../stores/files";
import { useLayoutStore } from "../../stores/layout";
import { useVoiceStore } from "../../stores/voice";
import { useWorkspacesStore } from "../../stores/workspaces";
import type { CodexSessionInfo } from "../../types/codex";

const emit = defineEmits<{
  "open-codex-session": [id: string];
}>();

const props = defineProps<{
  sessionIds: string[];
}>();

const codex = useCodexStore();
const files = useFilesStore();
const layout = useLayoutStore();
const voice = useVoiceStore();
const workspaces = useWorkspacesStore();
const codexError = ref("");
const visibleSessions = computed(() => {
  const ids = new Set(props.sessionIds);
  return codex.sessions.filter((session) => ids.has(session.id));
});

function voiceContextId(sessionId: string) {
  return `codex:${sessionId}:prompt`;
}

function hasVoicePending(sessionId: string) {
  const status = voice.context(voiceContextId(sessionId)).status;
  return status === "connecting" || status === "recording" || status === "processing";
}

function hasVoiceReady(sessionId: string) {
  return voice.hasReadyText(voiceContextId(sessionId));
}

function sessionStatusIndicator(session: CodexSessionInfo) {
  if (session.status === "failed") return "failed";
  if (session.status === "exited" && codex.unreadSessionIds.includes(session.id)) return "completed";
  if (session.status === "running") return "running";
  return "";
}

function sessionStatusTitle(session: CodexSessionInfo) {
  const indicator = sessionStatusIndicator(session);
  if (indicator === "failed") return "Codex run failed";
  if (indicator === "completed") return "Codex run finished";
  if (indicator === "running") return "Codex run is running";
  return "";
}

watch(
  visibleSessions,
  (sessions) => {
    for (const session of sessions) {
      if (session.status === "exited") codex.markRead(session.id);
    }
  },
  { immediate: true },
);

async function newCodexSession() {
  codexError.value = "";
  try {
    const session = await codex.create("", files.currentPath);
    emit("open-codex-session", session.id);
  } catch (err) {
    codexError.value = err instanceof Error ? err.message : String(err);
  }
}

async function closeCodexSession(id: string) {
  workspaces.forgetActiveCodexSession(id);
  layout.clearCodexSession(id);
}
</script>

<template>
  <div class="sidebar-panel">
    <div class="sidebar-section">
      <button class="btn btn-sm btn-primary panel-command" type="button" @click="newCodexSession">
        <i class="bi bi-stars"></i>
        <span>New Codex</span>
      </button>
      <div v-if="codexError" class="codex-error">{{ codexError }}</div>
    </div>

    <div class="sidebar-section list-section">
      <div class="section-title">Codex</div>
      <div v-if="!visibleSessions.length" class="empty-panel">No Codex sessions in this workspace</div>
      <div
        v-for="session in visibleSessions"
        :key="session.id"
        class="sidebar-row"
        :class="[
          {
            active: layout.openCodexSessionIds.includes(session.id),
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
        <button class="sidebar-row-main" type="button" @click="emit('open-codex-session', session.id)">
          <span class="sidebar-row-name">{{ session.title }}</span>
        </button>
        <button class="btn btn-sm icon-button sidebar-row-action" type="button" title="Remove from workspace" @click="closeCodexSession(session.id)">
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

.codex-error {
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
</style>
