<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { terminalSocketUrl } from "../../api/client";
import { useTerminalsStore } from "../../stores/terminals";
import type { TerminalInfo, TerminalSnapshot } from "../../types/terminals";

const props = defineProps<{ id: string }>();
const terminals = useTerminalsStore();
const terminal = ref<TerminalSnapshot | null>(null);
const error = ref("");
const terminalElement = ref<HTMLElement | null>(null);
const debugMode = import.meta.env.DEV || import.meta.env.VITE_VIEWER_DEBUG === "1";
type TerminalMessage =
  | { type: "snapshot"; terminal: TerminalSnapshot }
  | { type: "output"; data?: string; output_version?: number }
  | { type: "status"; terminal: TerminalInfo }
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

function applySnapshot(snapshot: TerminalSnapshot) {
  terminal.value = snapshot;
  hasSnapshot = true;
  appliedOutputVersion = snapshot.output_version ?? 0;
  const replay = pendingOutput
    .filter((item) => item.outputVersion > appliedOutputVersion)
    .sort((a, b) => a.outputVersion - b.outputVersion);
  pendingOutput = [];
  resetOutput(snapshot.output, () => {
    replay.forEach((item) => applyOutput(item.data, item.outputVersion));
  });
  terminals.upsert(snapshot);
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

function focusTerminal() {
  xterm?.focus();
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
    <div ref="terminalElement" class="terminal-surface" @pointerdown="focusTerminal"></div>
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
