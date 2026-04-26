<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { getTerminal, terminalSocketUrl } from "../../api/client";
import { useTerminalsStore } from "../../stores/terminals";
import type { TerminalInfo, TerminalSnapshot } from "../../types/terminals";

const props = defineProps<{ id: string }>();
const terminals = useTerminalsStore();
const terminal = ref<TerminalSnapshot | null>(null);
const error = ref("");
const terminalElement = ref<HTMLElement | null>(null);
let socket: WebSocket | null = null;
let xterm: Terminal | null = null;
let fitAddon: FitAddon | null = null;
let resizeObserver: ResizeObserver | null = null;
let dataDisposable: { dispose: () => void } | null = null;
let mounted = false;
let reconnectTimer: number | null = null;

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
  xterm.loadAddon(fitAddon);
  xterm.open(terminalElement.value);
  dataDisposable = xterm.onData((data) => {
    send(data);
  });
  resizeObserver = new ResizeObserver(resize);
  resizeObserver.observe(terminalElement.value);
  void nextTick(resize);
}

function disposeTerminal() {
  dataDisposable?.dispose();
  resizeObserver?.disconnect();
  dataDisposable = null;
  resizeObserver = null;
  fitAddon = null;
  xterm?.dispose();
  xterm = null;
}

function writeOutput(data: string) {
  ensureTerminal();
  xterm?.write(data);
}

function resetOutput(data: string) {
  ensureTerminal();
  if (!xterm) return;
  xterm?.reset();
  if (data) {
    xterm.write(data, () => {
      resize();
    });
    return;
  }
  resize();
}

function send(data: string) {
  if (socket?.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify({ type: "input", data }));
  }
}

function resize() {
  if (!fitAddon || !xterm) return;
  try {
    fitAddon.fit();
  } catch {
    return;
  }
  if (socket?.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify({ type: "resize", rows: xterm.rows, cols: xterm.cols }));
  }
}

function connect() {
  if (reconnectTimer !== null) {
    window.clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  socket?.close();
  const activeSocket = new WebSocket(terminalSocketUrl(props.id));
  socket = activeSocket;
  activeSocket.addEventListener("open", resize);
  activeSocket.addEventListener("message", (event) => {
    if (socket !== activeSocket) return;
    const message = JSON.parse(event.data) as { type: string; data?: string; terminal?: TerminalSnapshot | TerminalInfo };
    if (message.type === "snapshot" && message.terminal) {
      terminal.value = message.terminal as TerminalSnapshot;
      resetOutput(terminal.value.output);
      terminals.upsert(terminal.value);
    } else if (message.type === "output") {
      writeOutput(message.data ?? "");
    } else if (message.type === "status" && message.terminal) {
      terminals.upsert(message.terminal as TerminalInfo);
      if (terminal.value) {
        terminal.value.status = (message.terminal as TerminalInfo).status;
        terminal.value.exit_code = (message.terminal as TerminalInfo).exit_code;
      }
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
    terminal.value = await getTerminal(props.id);
    terminals.upsert(terminal.value);
    resetOutput(terminal.value.output);
    connect();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function endTerminal() {
  await terminals.terminate(props.id);
}

watch(() => props.id, () => {
  socket?.close();
  void load();
});
onMounted(() => {
  mounted = true;
  window.addEventListener("resize", resize);
  void nextTick(() => {
    ensureTerminal();
    void load();
  });
});
onUnmounted(() => {
  mounted = false;
  if (reconnectTimer !== null) window.clearTimeout(reconnectTimer);
  window.removeEventListener("resize", resize);
  socket?.close();
  disposeTerminal();
});
</script>

<template>
  <div v-if="error" class="terminal-error">{{ error }}</div>
  <div v-else class="terminal-viewer">
    <div class="terminal-bar">
      <span>{{ terminal?.shell ?? "zsh" }}</span>
      <span class="terminal-status" :class="terminal?.status">{{ terminal?.status ?? "connecting" }}</span>
      <button class="btn btn-sm btn-outline-light terminal-end" type="button" title="End terminal" @click="endTerminal">
        <i class="bi bi-stop-fill"></i>
      </button>
    </div>
    <div ref="terminalElement" class="terminal-surface"></div>
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
}

.terminal-bar {
  align-items: center;
  border-bottom: 1px solid #30363d;
  color: #8b949e;
  display: flex;
  flex: 0 0 34px;
  font-size: 12px;
  gap: 8px;
  padding: 4px 8px 4px 12px;
}

.terminal-status {
  border: 1px solid #30363d;
  border-radius: 999px;
  line-height: 1;
  padding: 3px 7px;
}

.terminal-status.running {
  color: #7ee787;
}

.terminal-end {
  height: 26px;
  margin-left: auto;
  width: 30px;
}

.terminal-surface {
  background: transparent;
  flex: 1 1 auto;
  min-height: 0;
  overflow: hidden;
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
</style>
