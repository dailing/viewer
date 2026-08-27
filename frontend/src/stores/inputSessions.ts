import { defineStore } from "pinia";

export type InputSessionPhase = "idle" | "sending" | "error";

export interface InputSession {
  id: string;
  pluginId: string;
  paneType: string;
  instanceId: string;
  label: string;
  text: string;
  selectedRoleIds: string[];
  forceNewSession: boolean;
  parallel: boolean;
  pinned: boolean;
  phase: InputSessionPhase;
  error: string;
  updatedAt: number;
}

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
        this.save();
        return existing;
      }
      const session: InputSession = {
        ...registration,
        text: "",
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
    setText(id: string, text: string): void { this.patch(id, { text }); },
    setPinned(id: string, pinned: boolean): void { this.patch(id, { pinned }); },
    clear(id: string): void {
      this.patch(id, { text: "", forceNewSession: false, parallel: false, phase: "idle", error: "" });
    },
    async send(id: string, textOverride?: string): Promise<boolean> {
      const session = this.sessions[id];
      if (session === undefined) return false;
      if (textOverride !== undefined) session.text = textOverride;
      if (!session.text.trim() || session.phase === "sending") {
        this.save();
        return false;
      }
      this.activate(id);
      this.patch(id, { phase: "sending", error: "" });
      try {
        const sender = runtimeSenders.get(id) ?? pluginSenders.get(session.pluginId);
        if (sender === undefined) throw new Error(`No input sender registered for ${session.pluginId}.`);
        const sent = await sender({ ...session, selectedRoleIds: [...session.selectedRoleIds] });
        if (sent === false) {
          this.patch(id, { phase: "error", error: "Dispatch failed; draft was preserved." });
          return false;
        }
        this.clear(id);
        return true;
      } catch (cause) {
        this.patch(id, { phase: "error", error: cause instanceof Error ? cause.message : String(cause) });
        return false;
      }
    },
  },
});

