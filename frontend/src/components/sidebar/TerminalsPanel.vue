<script setup lang="ts">
import { useLayoutStore } from "../../stores/layout";
import { useTerminalsStore } from "../../stores/terminals";
import { useVoiceStore } from "../../stores/voice";

const emit = defineEmits<{
  "open-terminal": [id: string];
}>();

const props = defineProps<{
  defaultCwd?: string;
}>();

const layout = useLayoutStore();
const terminals = useTerminalsStore();
const voice = useVoiceStore();

function voiceContextId(terminalId: string) {
  return `terminal:${terminalId}:paste`;
}

function hasVoicePending(terminalId: string) {
  const status = voice.context(voiceContextId(terminalId)).status;
  return status === "connecting" || status === "recording" || status === "processing";
}

function hasVoiceReady(terminalId: string) {
  return voice.hasReadyText(voiceContextId(terminalId));
}

async function newTerminal() {
  const terminal = await terminals.create(props.defaultCwd ?? "");
  emit("open-terminal", terminal.id);
}

async function closeTerminal(id: string) {
  await terminals.remove(id);
  layout.clearTerminal(id);
}
</script>

<template>
  <div class="sidebar-panel">
    <div class="sidebar-section">
      <button class="btn btn-sm btn-primary panel-command" type="button" @click="newTerminal">
        <i class="bi bi-terminal"></i>
        <span>New Terminal</span>
      </button>
    </div>

    <div class="sidebar-section list-section">
      <div class="section-title">Terminals</div>
      <div v-if="!terminals.terminals.length" class="empty-panel">No terminals</div>
      <div
        v-for="terminal in terminals.terminals"
        :key="terminal.id"
        class="sidebar-row"
        :class="{
          active: layout.openTerminalIds.includes(terminal.id),
          'voice-pending': hasVoicePending(terminal.id),
          'voice-ready': hasVoiceReady(terminal.id),
        }"
      >
        <button class="sidebar-row-main" type="button" @click="emit('open-terminal', terminal.id)">
          <i class="bi" :class="terminal.status === 'running' ? 'bi-terminal-fill' : 'bi-terminal'"></i>
          <span class="sidebar-row-name">{{ terminal.title }}</span>
        </button>
        <span class="state-pill" :class="terminal.status">{{ terminal.status }}</span>
        <button
          class="btn btn-sm icon-button sidebar-row-action"
          type="button"
          :title="terminals.isPinned(terminal.id) ? 'Unpin terminal' : 'Pin terminal'"
          @click="terminals.togglePin(terminal.id)"
        >
          <i class="bi" :class="terminals.isPinned(terminal.id) ? 'bi-pin-angle-fill' : 'bi-pin-angle'"></i>
        </button>
        <button class="btn btn-sm icon-button sidebar-row-action" type="button" title="Close terminal" @click="closeTerminal(terminal.id)">
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

.sidebar-row-action {
  flex: 0 0 auto;
  height: 24px;
  opacity: 0.75;
  width: 24px;
}

.state-pill {
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--text-muted);
  flex: 0 0 auto;
  font-size: 10px;
  line-height: 1;
  padding: 3px 6px;
}

.state-pill.running {
  border-color: #9fc5a8;
  color: #146c43;
}

.state-pill.failed {
  border-color: #e8c1c1;
  color: #a33;
}
</style>
