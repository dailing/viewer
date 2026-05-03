<script setup lang="ts">
import { computed, ref } from "vue";
import { useCodexStore } from "../../stores/codex";
import { useFilesStore } from "../../stores/files";
import { useLayoutStore } from "../../stores/layout";
import { useVoiceStore } from "../../stores/voice";
import type { CodexSessionInfo } from "../../types/codex";

const emit = defineEmits<{
  "open-codex-session": [id: string];
}>();

const codex = useCodexStore();
const files = useFilesStore();
const layout = useLayoutStore();
const voice = useVoiceStore();
const codexError = ref("");
const visibleSessions = computed(() => codex.sessions.filter((session) => sessionMatchesCurrentDirectory(session)));

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

function normalizeDirectory(path: string) {
  return path.replace(/\\/g, "/").replace(/^\/+|\/+$/g, "");
}

function matchesRelativeDirectory(sessionDirectory: string, currentDirectory: string) {
  const sessionPath = normalizeDirectory(sessionDirectory);
  const currentPath = normalizeDirectory(currentDirectory);
  if (!currentPath) return true;
  return sessionPath === currentPath || sessionPath.startsWith(`${currentPath}/`);
}

function absolutePathMatchesRelativeDirectory(sessionDirectory: string, currentDirectory: string) {
  const sessionPath = normalizeDirectory(sessionDirectory);
  const currentPath = normalizeDirectory(currentDirectory);
  if (!currentPath) return true;
  return sessionPath.endsWith(`/${currentPath}`) || sessionPath.includes(`/${currentPath}/`);
}

function sessionMatchesCurrentDirectory(session: CodexSessionInfo) {
  if (session.cwd_relative) {
    return matchesRelativeDirectory(session.cwd_relative, files.currentPath);
  }
  return absolutePathMatchesRelativeDirectory(session.cwd, files.currentPath);
}

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
  await codex.remove(id);
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
      <div v-if="!visibleSessions.length" class="empty-panel">No Codex sessions in this directory</div>
      <div
        v-for="session in visibleSessions"
        :key="session.id"
        class="sidebar-row"
        :class="[
          `status-${session.status}`,
          {
            active: layout.openCodexSessionIds.includes(session.id),
            'voice-pending': hasVoicePending(session.id),
            'voice-ready': hasVoiceReady(session.id),
          },
        ]"
      >
        <button class="sidebar-row-main" type="button" @click="emit('open-codex-session', session.id)">
          <span v-if="codex.unreadSessionIds.includes(session.id)" class="unread-dot" aria-label="Unread output"></span>
          <span class="sidebar-row-name">{{ session.title }}</span>
        </button>
        <button class="btn btn-sm icon-button sidebar-row-action" type="button" title="Delete Codex session" @click="closeCodexSession(session.id)">
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
  border: 0;
  border-radius: 6px;
  color: inherit;
  display: flex;
  gap: 7px;
  min-height: 30px;
  padding: 3px 6px;
  text-align: left;
  width: 100%;
}

.sidebar-row:hover,
.sidebar-row.active {
  background: #eef3f8;
}

.sidebar-row.status-running {
  background: #ffe4e4;
}

.sidebar-row.status-running:hover,
.sidebar-row.status-running.active {
  background: #ffd4d4;
}

.sidebar-row.status-failed {
  background: #fff0f0;
}

.sidebar-row.status-failed:hover,
.sidebar-row.status-failed.active {
  background: #f8dddd;
}

.sidebar-row.voice-pending {
  background: #fff6d7;
}

.sidebar-row.voice-pending:hover,
.sidebar-row.voice-pending.active {
  background: #ffedb0;
}

.sidebar-row.voice-ready {
  background: #fff0b8;
}

.sidebar-row.voice-ready:hover,
.sidebar-row.voice-ready.active {
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

.unread-dot {
  background: #d1242f;
  border-radius: 999px;
  flex: 0 0 auto;
  height: 7px;
  width: 7px;
}

.sidebar-row-action {
  flex: 0 0 auto;
  height: 24px;
  opacity: 0.75;
  width: 24px;
}
</style>
