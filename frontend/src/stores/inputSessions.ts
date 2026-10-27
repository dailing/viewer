import { defineStore } from "pinia";
import {
  beginQueuedInputEdit,
  cancelQueuedInputEdit,
  clearInputSession,
  completeInputSessionSend,
  inputSessionIntent,
  releaseQueuedInputEdit,
  setInputSessionBranch,
  setInputSessionText,
  snapshotInputSession,
} from "./inputSessionState";
import type { InputSession, InputSessionIntent, InputSessionMode } from "./inputSessionState";

export type { InputSession, InputSessionIntent, InputSessionMode, InputSessionPhase } from "./inputSessionState";

export interface InputSessionRegistration {
  id: string;
  pluginId: string;
  paneType: string;
  instanceId: string;
  label: string;
}

export type InputSessionSender = (session: InputSession) => Promise<boolean | void>;

const STORAGE_KEY = "viewer.inputSessions.v1";
const ACTIVE_KEY = "viewer.activeInputSession.v1";
const pluginSenders = new Map<string, InputSessionSender>();
const runtimeSenders = new Map<string, InputSessionSender>();

function loadSessions(): Record<string, InputSession> {
  try {
    const parsed = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "{}") as Record<string, Partial<InputSession>>;
    return Object.fromEntries(Object.entries(parsed).map(([id, value]) => [id, {
      id,
      pluginId: value.pluginId ?? id.split(":", 1)[0] ?? "",
      paneType: value.paneType ?? value.pluginId ?? "",
      instanceId: value.instanceId ?? "main",
      label: value.label ?? "Input",
      text: value.text ?? "",
      branchId: value.branchId ?? "",
      mode: value.mode === "queued-update" ? "queued-update" : "dispatch",
      queuedDispatchId: value.queuedDispatchId ?? "",
      draftBeforeQueueEdit: value.draftBeforeQueueEdit,
      revision: typeof value.revision === "number" ? value.revision : 0,
      selectedRoleIds: Array.isArray(value.selectedRoleIds) ? value.selectedRoleIds.filter((item): item is string => typeof item === "string") : [],
      forceNewSession: value.forceNewSession === true,
      parallel: value.parallel === true,
      pinned: value.pinned !== false,
      phase: value.phase === "error" ? "error" : "idle",
      error: value.phase === "error" ? value.error ?? "" : "",
      updatedAt: typeof value.updatedAt === "number" ? value.updatedAt : Date.now(),
    } satisfies InputSession]));
  } catch {
    return {};
  }
}

function persist(sessions: Record<string, InputSession>, activeId: string): void {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(sessions));
  if (activeId) localStorage.setItem(ACTIVE_KEY, activeId);
  else localStorage.removeItem(ACTIVE_KEY);
}

export function registerInputSessionSender(pluginId: string, sender: InputSessionSender): () => void {
  pluginSenders.set(pluginId, sender);
  return () => { if (pluginSenders.get(pluginId) === sender) pluginSenders.delete(pluginId); };
}

export function registerInputSessionRuntime(id: string, sender: InputSessionSender): () => void {
  runtimeSenders.set(id, sender);
  return () => { if (runtimeSenders.get(id) === sender) runtimeSenders.delete(id); };
}

export const useInputSessionsStore = defineStore("inputSessions", {
  state: () => ({
    sessions: loadSessions(),
    activeId: localStorage.getItem(ACTIVE_KEY) ?? "",
  }),
  getters: {
    session: (state) => (id: string): InputSession | undefined => state.sessions[id],
    activeSession(state): InputSession | undefined { return state.sessions[state.activeId]; },
  },
  actions: {
    save(): void { persist(this.sessions, this.activeId); },
    ensure(registration: InputSessionRegistration): InputSession {
      const existing = this.sessions[registration.id];
      if (existing !== undefined) {
        Object.assign(existing, registration);
        existing.branchId ??= "";
        existing.mode ??= "dispatch";
        existing.queuedDispatchId ??= "";
        existing.revision ??= 0;
        this.save();
        return existing;
      }
      const session: InputSession = {
        ...registration,
        text: "",
        branchId: "",
        mode: "dispatch",
        queuedDispatchId: "",
        revision: 0,
        selectedRoleIds: [],
        forceNewSession: false,
        parallel: false,
        pinned: true,
        phase: "idle",
        error: "",
        updatedAt: Date.now(),
      };
      this.sessions[registration.id] = session;
      this.save();
      return session;
    },
    activate(id: string): void {
      if (this.sessions[id] === undefined) return;
      this.activeId = id;
      this.sessions[id].updatedAt = Date.now();
      this.save();
    },
    patch(id: string, patch: Partial<Omit<InputSession, "id">>): void {
      const session = this.sessions[id];
      if (session === undefined) return;
      Object.assign(session, patch, { updatedAt: Date.now() });
      this.save();
    },
    setText(id: string, text: string): void {
      const session = this.sessions[id];
      if (session === undefined) return;
      setInputSessionText(session, text);
      this.save();
    },
    setBranch(id: string, branchId: string): void {
      const session = this.sessions[id];
      if (session === undefined) return;
      setInputSessionBranch(session, branchId);
      this.save();
    },
    intent(id: string): InputSessionIntent | undefined {
      const session = this.sessions[id];
      return session === undefined ? undefined : inputSessionIntent(session);
    },
    beginQueuedEdit(id: string, branchId: string, dispatchId: string, text: string): void {
      const session = this.sessions[id];
      if (session === undefined) return;
      beginQueuedInputEdit(session, branchId, dispatchId, text);
      this.save();
    },
    cancelQueuedEdit(id: string): void {
      const session = this.sessions[id];
      if (session === undefined) return;
      cancelQueuedInputEdit(session);
      this.save();
    },
    releaseQueuedEdit(id: string): void {
      const session = this.sessions[id];
      if (session === undefined) return;
      releaseQueuedInputEdit(session);
      this.save();
    },
    setPinned(id: string, pinned: boolean): void { this.patch(id, { pinned }); },
    clear(id: string): void {
      const session = this.sessions[id];
      if (session === undefined) return;
      clearInputSession(session);
      this.save();
    },
    async send(id: string, textOverride?: string, intent?: InputSessionIntent): Promise<boolean> {
      const session = this.sessions[id];
      if (session === undefined) return false;
      const currentIntent = inputSessionIntent(session);
      const sameTarget = intent === undefined || (
        currentIntent.branchId === intent.branchId
        && currentIntent.mode === intent.mode
        && currentIntent.queuedDispatchId === intent.queuedDispatchId
      );
      if (textOverride !== undefined && sameTarget) setInputSessionText(session, textOverride);
      const snapshot = snapshotInputSession(session, textOverride ?? session.text, intent ?? currentIntent);
      if (!snapshot.text.trim() || session.phase === "sending") {
        this.save();
        return false;
      }
      this.activate(id);
      this.patch(id, { phase: "sending", error: "" });
      try {
        const sender = runtimeSenders.get(id) ?? pluginSenders.get(session.pluginId);
        if (sender === undefined) throw new Error(`No input sender registered for ${session.pluginId}.`);
        const sent = await sender(snapshot);
        if (sent === false) {
          this.patch(id, { phase: "error", error: "Dispatch failed; draft was preserved." });
          return false;
        }
        completeInputSessionSend(session, snapshot);
        this.save();
        return true;
      } catch (cause) {
        this.patch(id, { phase: "error", error: cause instanceof Error ? cause.message : String(cause) });
        return false;
      }
    },
  },
});
