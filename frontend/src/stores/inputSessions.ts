import { defineStore } from "pinia";
import { createSuperWorkspaceRun } from "../api/client";
import { useSuperChatComposerStore } from "./superChatComposer";
import { useVoiceStore, type VoiceJobStatus } from "./voice";

export type InputSubmitTarget =
  | {
      type: "super-chat";
      chatId: string;
      roleIds?: string[];
    };

export type InputContextRegistration = {
  id: string;
  label: string;
  kind: "super-chat" | "generic";
  ownerId?: string;
  submitTarget?: InputSubmitTarget;
};

export type PendingInputSend = {
  contextId: string;
  status: "processing" | "sending" | "failed";
  requestedAt: number;
  error?: string;
};

export type GlobalInputStatus = {
  visible: boolean;
  contextId: string;
  label: string;
  detail: string;
  status: VoiceJobStatus | "sending" | "pending" | "failed";
  canSend: boolean;
  busy: boolean;
  error: string;
};

function wait(ms: number) {
  return new Promise<void>((resolve) => window.setTimeout(resolve, ms));
}

export const useInputSessionsStore = defineStore("inputSessions", {
  state: () => ({
    contexts: {} as Record<string, InputContextRegistration>,
    pendingSends: {} as Record<string, PendingInputSend>,
  }),
  getters: {
    globalStatus(state): GlobalInputStatus {
      const voice = useVoiceStore();
      const pending = Object.values(state.pendingSends).sort((left, right) => right.requestedAt - left.requestedAt)[0];
      const contextId = voice.activeRecordingContextId || pending?.contextId || "";
      if (!contextId) {
        return { visible: false, contextId: "", label: "", detail: "", status: "idle", canSend: false, busy: false, error: "" };
      }
      const context = state.contexts[contextId];
      const voiceState = voice.context(contextId);
      const pendingSend = state.pendingSends[contextId];
      const status = pendingSend?.status === "sending" ? "sending" : pendingSend?.status === "failed" ? "failed" : voiceState.status;
      const hasSubmit = Boolean(context?.submitTarget);
      const detail = pendingSend
        ? pendingSend.status === "sending"
          ? "sending"
          : pendingSend.status === "failed"
            ? "send failed"
            : hasSubmit
              ? "will send after processing"
              : "will finish after processing"
        : voiceState.status === "recording"
          ? hasSubmit
            ? "recording for chat"
            : "recording"
          : voiceState.status === "processing"
            ? "processing voice"
            : voiceState.status === "ready"
              ? hasSubmit
                ? "ready to send"
                : "voice text ready"
              : voiceState.status === "error"
                ? "voice error"
                : "";
      return {
        visible: voiceState.status !== "idle" || Boolean(pendingSend),
        contextId,
        label: context?.label || "Voice input",
        detail,
        status,
        canSend: voiceState.status !== "idle" && pendingSend?.status !== "sending",
        busy: voiceState.status === "connecting" || voiceState.status === "recording" || voiceState.status === "processing" || pendingSend?.status === "sending",
        error: pendingSend?.error || voiceState.error || "",
      };
    },
  },
  actions: {
    registerContext(context: InputContextRegistration) {
      if (!context.id) return;
      this.contexts = { ...this.contexts, [context.id]: context };
    },
    async requestGlobalSend() {
      const status = this.globalStatus;
      if (!status.contextId) return;
      await this.requestSend(status.contextId);
    },
    async requestSend(contextId: string) {
      const voice = useVoiceStore();
      const context = this.contexts[contextId];
      const requestedAt = Date.now();
      this.pendingSends = {
        ...this.pendingSends,
        [contextId]: { contextId, status: "processing", requestedAt },
      };
      try {
        const currentStatus = voice.context(contextId).status;
        if (currentStatus === "connecting" || currentStatus === "recording") {
          await voice.stop(contextId);
        }
        const text = await this.waitForFinalText(contextId);
        if (!context?.submitTarget) {
          this.clearPending(contextId);
          return;
        }
        if (!text.trim()) {
          this.failPending(contextId, "Nothing to send.");
          return;
        }
        this.pendingSends = {
          ...this.pendingSends,
          [contextId]: { contextId, status: "sending", requestedAt },
        };
        if (context.submitTarget.type === "super-chat") {
          await createSuperWorkspaceRun({
            message: text.trim(),
            chat_id: context.submitTarget.chatId,
            role_ids: context.submitTarget.roleIds?.length ? context.submitTarget.roleIds : undefined,
          });
          useSuperChatComposerStore().clearDraft(context.submitTarget.chatId);
          voice.clear(contextId);
        }
        this.clearPending(contextId);
      } catch (error) {
        this.failPending(contextId, error instanceof Error ? error.message : String(error));
      }
    },
    async waitForFinalText(contextId: string): Promise<string> {
      const voice = useVoiceStore();
      const deadline = Date.now() + 125000;
      while (Date.now() < deadline) {
        const state = voice.context(contextId);
        if (state.status === "ready" || state.status === "idle") return state.text;
        if (state.status === "error") throw new Error(state.error || "Voice input failed.");
        await wait(250);
      }
      throw new Error("Voice processing timed out.");
    },
    clearPending(contextId: string) {
      const next = { ...this.pendingSends };
      delete next[contextId];
      this.pendingSends = next;
    },
    failPending(contextId: string, error: string) {
      const current = this.pendingSends[contextId];
      this.pendingSends = {
        ...this.pendingSends,
        [contextId]: {
          contextId,
          status: "failed",
          requestedAt: current?.requestedAt ?? Date.now(),
          error,
        },
      };
    },
  },
});
