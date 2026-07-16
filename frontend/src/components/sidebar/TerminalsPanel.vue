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
.sidebar-row.voice-pending {
  background: color-mix(in srgb, var(--color-warning) 10%, var(--color-surface));
}

.sidebar-row.voice-pending:hover {
  background: color-mix(in srgb, var(--color-warning) 20%, var(--color-surface));
}

.sidebar-row.voice-ready {
  background: color-mix(in srgb, var(--color-warning) 24%, var(--color-surface));
}

.sidebar-row.voice-ready:hover {
  background: color-mix(in srgb, var(--color-warning) 34%, var(--color-surface));
}

.state-pill {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  flex: 0 0 auto;
  font-size: 10px;
  line-height: 1;
  padding: 3px 6px;
}

.state-pill.running {
  border-color: color-mix(in srgb, var(--color-success) 40%, var(--color-border));
  color: var(--color-success);
}

.state-pill.failed {
  border-color: color-mix(in srgb, var(--color-danger) 38%, var(--color-border));
  color: var(--color-danger);
}
</style>
