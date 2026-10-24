import { reactive, watch } from "vue";

/**
 * Per-line raw mode (framework v0.92): while a line's raw mode is on, its
 * sends carry `raw: true` — the backend skips every viewer-side prompt
 * injection (workspace/chat common prompt, role prompt, line context, the
 * role-switch bridge) and the agent receives the cited section plus the
 * message itself, like driving it from its own CLI through the viewer.
 * Sessions resume as usual and the write side (summaries, memory retain) is
 * untouched, so turning raw off again restores full injection seamlessly.
 *
 * State is browser-local and keyed by chat + branch ("" = mainline): every
 * line remembers its own mode, matching how the composer binds drafts and
 * toggles to the send-target line.
 */
const STORAGE_KEY = "viewer.chatRaw.v1";

function load(): Record<string, boolean> {
  try {
    const parsed = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "{}") as Record<string, unknown>;
    return Object.fromEntries(
      Object.entries(parsed)
        .filter(([, value]) => value === true)
        .map(([key]) => [key, true]),
    );
  } catch {
    return {};
  }
}

const states = reactive<Record<string, boolean>>(load());
watch(
  states,
  () => localStorage.setItem(STORAGE_KEY, JSON.stringify(states)),
  { deep: true },
);

export function rawModeEnabled(chatId: string, branchId: string): boolean {
  return states[`${chatId}:${branchId}`] === true;
}

export function setRawMode(chatId: string, branchId: string, raw: boolean): void {
  const key = `${chatId}:${branchId}`;
  if (raw) states[key] = true;
  else delete states[key];
}
