<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { terminalSocketUrl } from "../../api/client";
import VoiceTextarea from "../VoiceTextarea.vue";
import { usePaneToolbarStore } from "../../stores/paneToolbar";
import { useLayoutStore } from "../../stores/layout";
import { useTerminalsStore } from "../../stores/terminals";
import { useVoiceStore } from "../../stores/voice";
import type { PaneToolbarAction } from "../../stores/paneToolbar";
import type { TerminalInfo, TerminalSnapshot } from "../../types/terminals";

const props = defineProps<{ id: string; paneId: string }>();
const paneToolbar = usePaneToolbarStore();
const layout = useLayoutStore();
const terminals = useTerminalsStore();
const voice = useVoiceStore();
const terminal = ref<TerminalSnapshot | null>(null);
const error = ref("");
const terminalElement = ref<HTMLElement | null>(null);
const pastePadTextarea = ref<InstanceType<typeof VoiceTextarea> | null>(null);
const controlLatch = ref(false);
const pastePadOpen = ref(false);
const pastePadText = ref("");
const debugMode = import.meta.env.DEV || import.meta.env.VITE_VIEWER_DEBUG === "1";
type TerminalMessage =
  | { type: "snapshot"; terminal: TerminalSnapshot }
  | { type: "output"; data?: string; output_version?: number }
  | { type: "status"; terminal: TerminalInfo }
  | { type: "layout"; terminal: TerminalInfo }
  | { type: string; data?: string; output_version?: number; terminal?: TerminalSnapshot | TerminalInfo };
let socket: WebSocket | null = null;
let xterm: Terminal | null = null;
let fitAddon: FitAddon | null = null;
let resizeObserver: ResizeObserver | null = null;
let dataDisposable: { dispose: () => void } | null = null;
let parserDisposables: Array<{ dispose: () => void }> = [];
let mounted = false;
let reconnectTimer: number | null = null;
let hasSnapshot = false;
let appliedOutputVersion = 0;
let pendingOutput: Array<{ data: string; outputVersion: number }> = [];
let deferredOutput: Array<{ data: string; outputVersion?: number }> = [];
let modeQueryRemainder = "";
let resettingOutput = false;
let suppressPtyInput = false;
let applyingRemoteLayout = false;
let slowSendToken = 0;
let lastTouchTap: { time: number; x: number; y: number } | null = null;
const isActivePane = computed(() => layout.activePaneId === props.paneId);
const voiceContextId = computed(() => `terminal:${props.id}:paste`);

type SoftKey = {
  title: string;
  data: string;
  icon?: string;
  label?: string;
};

const SOFTKEYS: SoftKey[] = [
  { title: "Tab", label: "Tab", data: "\x09" },
  { title: "Interrupt (Ctrl+C)", icon: "bi-x-octagon", data: "\x03" },
  { title: "EOF (Ctrl+D)", icon: "bi-box-arrow-right", data: "\x04" },
  { title: "Home", icon: "bi-chevron-bar-left", data: "\x01" },
  { title: "End", icon: "bi-chevron-bar-right", data: "\x05" },
  { title: "Page up", icon: "bi-chevron-double-up", data: "\x1b[5~" },
  { title: "Page down", icon: "bi-chevron-double-down", data: "\x1b[6~" },
  { title: "Left", icon: "bi-arrow-left", data: "\x1b[D" },
  { title: "Down", icon: "bi-arrow-down", data: "\x1b[B" },
  { title: "Up", icon: "bi-arrow-up", data: "\x1b[A" },
  { title: "Right", icon: "bi-arrow-right", data: "\x1b[C" },
];

function firstParam(params: Array<number | number[]>): number {
  const first = params[0];
  return Array.isArray(first) ? first[0] ?? 0 : first ?? 0;
}

function registerModeQueryHandlers(term: Terminal) {
  const reply = (params: Array<number | number[]>, privateMode: boolean) => {
    send(`\x1b[${privateMode ? "?" : ""}${firstParam(params)};0$y`);
    return true;
  };
  parserDisposables = [
    term.parser.registerCsiHandler({ intermediates: "$", final: "p" }, (params) => reply(params, false)),
    term.parser.registerCsiHandler({ prefix: "?", intermediates: "$", final: "p" }, (params) => reply(params, true)),
  ];
}

function modeQueryReply(sequence: string): string | null {
  const match = /^\x1b\[(\??)([0-9;:]*)\$p$/.exec(sequence);
  if (!match) return null;
  const mode = Number.parseInt(match[2].split(/[;:]/, 1)[0] || "0", 10) || 0;
  return `\x1b[${match[1]}${mode};0$y`;
}

function filterModeQueries(data: string, respond: boolean): string {
  const input = modeQueryRemainder + data;
  modeQueryRemainder = "";
  let output = "";
  let offset = 0;

  while (offset < input.length) {
    const start = input.indexOf("\x1b[", offset);
    if (start === -1) {
      output += input.slice(offset);
      break;
    }
    output += input.slice(offset, start);

    let end = start + 2;
    while (end < input.length) {
      const code = input.charCodeAt(end);
      if (code >= 0x40 && code <= 0x7e) break;
      end += 1;
    }
    if (end >= input.length) {
      modeQueryRemainder = input.slice(start);
      break;
    }

    const sequence = input.slice(start, end + 1);
    const reply = modeQueryReply(sequence);
    if (reply) {
      if (respond) send(reply);
    } else {
      output += sequence;
    }
    offset = end + 1;
  }

  if (modeQueryRemainder.length > 128) {
    output += modeQueryRemainder;
    modeQueryRemainder = "";
  }
  return output;
}

function ensureTerminal() {
  if (xterm || !terminalElement.value) return;
  fitAddon = new FitAddon();
  xterm = new Terminal({
    allowProposedApi: false,
    cursorBlink: true,
    convertEol: false,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace',
    fontSize: 13,
    lineHeight: 1.25,
    logLevel: debugMode ? "debug" : "error",
    scrollback: 10000,
    theme: {
      background: "#0d1117",
      foreground: "#d6deeb",
      cursor: "#d6deeb",
      selectionBackground: "#31516f",
      black: "#484f58",
      red: "#ff7b72",
      green: "#7ee787",
      yellow: "#d29922",
      blue: "#79c0ff",
      magenta: "#d2a8ff",
      cyan: "#56d4dd",
      white: "#b1bac4",
      brightBlack: "#6e7681",
      brightRed: "#ffa198",
      brightGreen: "#56d364",
      brightYellow: "#e3b341",
      brightBlue: "#a5d6ff",
      brightMagenta: "#d2a8ff",
      brightCyan: "#76e3ea",
      brightWhite: "#f0f6fc",
    },
  });
  registerModeQueryHandlers(xterm);
  xterm.loadAddon(fitAddon);
  xterm.open(terminalElement.value);
  xterm.attachCustomKeyEventHandler((event) => {
    if (event.type === "keydown" && controlLatch.value && !event.altKey && !event.metaKey) {
      if (event.key.length === 1) {
        event.preventDefault();
        send(controlSequence(event.key));
        controlLatch.value = false;
        return false;
      }
      if (event.key === "Escape") {
        controlLatch.value = false;
        return true;
      }
    }
    if (event.type === "keydown" && !event.ctrlKey && !event.altKey && !event.metaKey) {
      if (event.key === "Home") {
        event.preventDefault();
        send("\x01");
        return false;
      }
      if (event.key === "End") {
        event.preventDefault();
        send("\x05");
        return false;
      }
    }
    if (
      event.type === "keydown" &&
      event.ctrlKey &&
      !event.altKey &&
      !event.metaKey &&
      !event.shiftKey &&
      event.key.toLowerCase() === "c" &&
      !xterm?.hasSelection()
    ) {
      event.preventDefault();
      send("\x03");
      return false;
    }
    return true;
  });
  dataDisposable = xterm.onData((data) => {
    if (suppressPtyInput) return;
    send(data);
  });
  resizeObserver = new ResizeObserver(resize);
  resizeObserver.observe(terminalElement.value);
  void nextTick(resize);
}

function disposeTerminal() {
  parserDisposables.forEach((disposable) => disposable.dispose());
  dataDisposable?.dispose();
  resizeObserver?.disconnect();
  parserDisposables = [];
  dataDisposable = null;
  resizeObserver = null;
  fitAddon = null;
  xterm?.dispose();
  xterm = null;
  resettingOutput = false;
  suppressPtyInput = false;
}

function writeOutput(data: string) {
  ensureTerminal();
  const output = filterModeQueries(data, true);
  if (output) xterm?.write(output);
}

function resetOutput(data: string, afterReset?: () => void) {
  ensureTerminal();
  if (!xterm) return;
  modeQueryRemainder = "";
  resettingOutput = true;
  suppressPtyInput = true;
  const finishReset = () => {
    suppressPtyInput = false;
    resettingOutput = false;
    resize();
    afterReset?.();
    const deferred = deferredOutput;
    deferredOutput = [];
    deferred.forEach((item) => applyOutput(item.data, item.outputVersion));
  };
  xterm.reset();
  xterm.clear();
  const output = filterModeQueries(data, false);
  modeQueryRemainder = "";
  if (output) {
    xterm.write(output, () => {
      finishReset();
    });
    return;
  }
  finishReset();
}

function applyLockedLayout() {
  if (!xterm || !terminal.value?.layout_locked) return;
  applyingRemoteLayout = true;
  try {
    if (xterm.rows !== terminal.value.rows || xterm.cols !== terminal.value.cols) {
      xterm.resize(terminal.value.cols, terminal.value.rows);
    }
  } finally {
    applyingRemoteLayout = false;
  }
}

function applyTerminalInfo(info: TerminalInfo) {
  terminals.upsert(info);
  if (!terminal.value) return;
  terminal.value.status = info.status;
  terminal.value.exit_code = info.exit_code;
  terminal.value.rows = info.rows;
  terminal.value.cols = info.cols;
  terminal.value.layout_locked = info.layout_locked;
  applyLockedLayout();
}

function applySnapshot(snapshot: TerminalSnapshot) {
  terminal.value = snapshot;
  hasSnapshot = true;
  appliedOutputVersion = snapshot.output_version ?? 0;
  // Fit before replay to avoid wrapping artifacts after remount/view switches.
  scheduleResize();
  const replay = pendingOutput
    .filter((item) => item.outputVersion > appliedOutputVersion)
    .sort((a, b) => a.outputVersion - b.outputVersion);
  pendingOutput = [];
  resetOutput(snapshot.output, () => {
    scheduleResize();
    replay.forEach((item) => applyOutput(item.data, item.outputVersion));
  });
  terminals.upsert(snapshot);
  applyLockedLayout();
}

function applyOutput(data: string, outputVersion?: number) {
  if (!hasSnapshot) {
    pendingOutput.push({ data, outputVersion: outputVersion ?? Number.MAX_SAFE_INTEGER });
    return;
  }
  if (resettingOutput) {
    deferredOutput.push({ data, outputVersion });
    return;
  }
  if (outputVersion !== undefined) {
    if (outputVersion <= appliedOutputVersion) return;
    appliedOutputVersion = outputVersion;
  }
  writeOutput(data);
}

function send(data: string) {
  if (socket?.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify({ type: "input", data }));
  }
}

function normalizeTerminalInput(data: string): string {
  return data.replace(/\r\n/g, "\n").replace(/\n/g, "\r");
}

function controlSequence(key: string): string {
  const normalized = key.toUpperCase();
  if (normalized.length !== 1) return key;
  return String.fromCharCode(normalized.charCodeAt(0) & 0x1f);
}

function sendSoftInput(data: string) {
  send(data);
  xterm?.focus();
}

function openPastePad() {
  pastePadOpen.value = true;
  void nextTick(() => pastePadTextarea.value?.focus());
}

function closePastePad() {
  pastePadOpen.value = false;
  xterm?.focus();
}

function clearPastePad() {
  pastePadText.value = "";
  voice.clear(voiceContextId.value);
  void nextTick(() => pastePadTextarea.value?.focus());
}

function sendPastePadText(extra = "") {
  const data = normalizeTerminalInput(pastePadText.value) + extra;
  if (!data) return;
  send(data);
  pastePadText.value = "";
  voice.clear(voiceContextId.value);
  closePastePad();
}

function sendBracketedPaste() {
  const data = normalizeTerminalInput(pastePadText.value);
  if (!data) return;
  send(`\x1b[200~${data}\x1b[201~`);
  pastePadText.value = "";
  voice.clear(voiceContextId.value);
  closePastePad();
}

async function sendSlowPaste() {
  const data = normalizeTerminalInput(pastePadText.value);
  if (!data) return;
  const token = ++slowSendToken;
  pastePadOpen.value = false;
  for (let index = 0; index < data.length && token === slowSendToken; index += 32) {
    send(data.slice(index, index + 32));
    await new Promise((resolve) => window.setTimeout(resolve, 25));
  }
  if (token === slowSendToken) {
    pastePadText.value = "";
    voice.clear(voiceContextId.value);
  }
  xterm?.focus();
}

function sendShortcut(data: string) {
  sendSoftInput(data);
  controlLatch.value = false;
}

function toggleControlLatch() {
  controlLatch.value = !controlLatch.value;
  xterm?.focus();
}

function updatePaneToolbar() {
  const status = terminal.value?.status ?? "connecting";
  const actions: PaneToolbarAction[] = [
    {
      id: "terminal-paste-pad",
      title: "Open text input pad",
      icon: "bi-textarea-t",
      active: pastePadOpen.value,
      run: openPastePad,
    },
    {
      id: "terminal-ctrl",
      title: "Latch Ctrl for the next typed key",
      label: "Ctrl",
      active: controlLatch.value,
      run: toggleControlLatch,
    },
    ...SOFTKEYS.map((key) => ({
      id: `terminal-${key.title.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`,
      title: key.title,
      icon: key.icon,
      label: key.label,
      run: () => sendShortcut(key.data),
    })),
    {
      id: "terminal-end",
      title: "End terminal",
      icon: "bi-stop-fill",
      variant: "danger" as const,
      run: endTerminal,
    },
  ];

  paneToolbar.setPaneToolbar(props.paneId, {
    title: terminal.value?.shell ?? "zsh",
    status,
    statusClass: status,
    actions,
  });
}

function resize() {
  if (!fitAddon || !xterm || applyingRemoteLayout) return;
  try {
    fitAddon.fit();
  } catch {
    return;
  }
  if (isActivePane.value && socket?.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify({ type: "resize", rows: xterm.rows, cols: xterm.cols }));
  }
}

function scheduleResize() {
  void nextTick(() => {
    window.requestAnimationFrame(() => {
      resize();
    });
  });
}

function focusTerminal() {
  xterm?.focus();
}

function handleTerminalDoubleClick() {
  openPastePad();
}

function handleTerminalPointerUp(event: PointerEvent) {
  if (event.pointerType === "mouse") return;
  const now = window.performance.now();
  const previous = lastTouchTap;
  lastTouchTap = { time: now, x: event.clientX, y: event.clientY };
  if (!previous) return;
  const elapsed = now - previous.time;
  const distance = Math.hypot(event.clientX - previous.x, event.clientY - previous.y);
  if (elapsed < 380 && distance < 24) {
    openPastePad();
    lastTouchTap = null;
  }
}

function connect() {
  if (reconnectTimer !== null) {
    window.clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  socket?.close();
  hasSnapshot = false;
  appliedOutputVersion = 0;
  pendingOutput = [];
  deferredOutput = [];
  modeQueryRemainder = "";
  resettingOutput = false;
  suppressPtyInput = false;
  const activeSocket = new WebSocket(terminalSocketUrl(props.id));
  socket = activeSocket;
  activeSocket.addEventListener("open", resize);
  activeSocket.addEventListener("message", (event) => {
    if (socket !== activeSocket) return;
    const message = JSON.parse(event.data) as TerminalMessage;
    if (message.type === "snapshot" && message.terminal) {
      applySnapshot(message.terminal as TerminalSnapshot);
    } else if (message.type === "output") {
      applyOutput(message.data ?? "", message.output_version);
    } else if (message.type === "status" && message.terminal) {
      applyTerminalInfo(message.terminal as TerminalInfo);
    } else if (message.type === "layout" && message.terminal) {
      applyTerminalInfo(message.terminal as TerminalInfo);
    }
  });
  activeSocket.addEventListener("close", () => {
    if (socket !== activeSocket) return;
    socket = null;
    if (mounted && terminal.value?.status !== "exited") {
      reconnectTimer = window.setTimeout(connect, 1000);
    }
  });
  activeSocket.addEventListener("error", () => {
    if (socket === activeSocket) activeSocket.close();
  });
}

async function load() {
  error.value = "";
  try {
    connect();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function handleRefresh(event: Event) {
  const paneId = (event as CustomEvent<{ paneId?: string }>).detail?.paneId;
  if (paneId !== props.paneId) return;
  terminal.value = null;
  hasSnapshot = false;
  appliedOutputVersion = 0;
  pendingOutput = [];
  deferredOutput = [];
  resetOutput("");
  void load();
}

async function endTerminal() {
  await terminals.terminate(props.id);
}

watch(() => props.id, () => {
  socket?.close();
  void load();
});
watch(() => props.paneId, (paneId, oldPaneId) => {
  if (oldPaneId && oldPaneId !== paneId) paneToolbar.clearPaneToolbar(oldPaneId);
  updatePaneToolbar();
});
watch(
  () => isActivePane.value,
  (active) => {
    if (active) resize();
  },
);
watch(
  () => [terminal.value?.shell, terminal.value?.status, terminal.value?.layout_locked, controlLatch.value, pastePadOpen.value] as const,
  updatePaneToolbar,
  { immediate: true },
);
onMounted(() => {
  mounted = true;
  window.addEventListener("focus", resize);
  window.addEventListener("resize", resize);
  window.addEventListener("viewer:pane-refresh", handleRefresh);
  void nextTick(() => {
    ensureTerminal();
    scheduleResize();
    void load();
  });
});
onUnmounted(() => {
  mounted = false;
  slowSendToken += 1;
  if (reconnectTimer !== null) window.clearTimeout(reconnectTimer);
  window.removeEventListener("focus", resize);
  window.removeEventListener("resize", resize);
  window.removeEventListener("viewer:pane-refresh", handleRefresh);
  socket?.close();
  paneToolbar.clearPaneToolbar(props.paneId);
  disposeTerminal();
});
</script>

<template>
  <div v-if="error" class="terminal-error">{{ error }}</div>
  <div v-else class="terminal-viewer">
    <div
      ref="terminalElement"
      class="terminal-surface"
      @pointerdown="focusTerminal"
      @pointerup="handleTerminalPointerUp"
      @dblclick="handleTerminalDoubleClick"
    ></div>
    <div v-if="pastePadOpen" class="terminal-paste-pad" @keydown.esc.stop.prevent="closePastePad">
      <div class="terminal-paste-pad-header">
        <span>Text input</span>
        <button class="btn btn-sm btn-outline-secondary icon-button" type="button" title="Close" @click="closePastePad">
          <i class="bi bi-x"></i>
        </button>
      </div>
      <VoiceTextarea ref="pastePadTextarea" v-model="pastePadText" :context-id="voiceContextId" min-height="92px" @clear="clearPastePad">
        <template #actions>
          <button class="btn btn-sm btn-primary" type="button" @click="sendPastePadText()">Send</button>
          <button class="btn btn-sm btn-outline-primary" type="button" @click="sendPastePadText('\r')">Send + Enter</button>
          <button class="btn btn-sm btn-outline-secondary" type="button" @click="sendBracketedPaste">Bracketed</button>
          <button class="btn btn-sm btn-outline-secondary" type="button" @click="sendSlowPaste">Slow</button>
        </template>
      </VoiceTextarea>
    </div>
  </div>
</template>

<style scoped>
.terminal-viewer {
  background: #0d1117;
  color: #d6deeb;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  position: relative;
}

.terminal-surface {
  background: transparent;
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
  padding: 8px;
}

.terminal-surface :deep(.xterm) {
  height: 100%;
}

.terminal-surface :deep(.xterm-viewport) {
  background: transparent;
}

.terminal-error {
  align-items: center;
  color: #a33;
  display: flex;
  height: 100%;
  justify-content: center;
  padding: 18px;
  text-align: center;
}

.terminal-paste-pad {
  background: #ffffff;
  border-top: 1px solid #2f3b4f;
  bottom: 0;
  box-shadow: 0 -8px 24px rgb(0 0 0 / 0.35);
  color: #172033;
  display: flex;
  flex-direction: column;
  gap: 8px;
  left: 0;
  max-height: min(52%, 360px);
  padding: 8px;
  position: absolute;
  right: 0;
  z-index: 5;
}

.terminal-paste-pad-header {
  align-items: center;
  display: flex;
  flex: 0 0 auto;
  font-size: 12px;
  font-weight: 700;
  gap: 8px;
}

.terminal-paste-pad-header span {
  flex: 1 1 auto;
  min-width: 0;
}

.terminal-paste-pad :deep(.voice-textarea) {
  flex: 1 1 auto;
  min-height: 0;
  padding-bottom: env(safe-area-inset-bottom);
}
</style>
