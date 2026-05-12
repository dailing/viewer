import type { CodexEvent, CodexPrompt, CodexQueueItem } from "./codex";

export type AgentProvider = string;
export type AgentStatus = "idle" | "running" | "exited" | "failed";

export type AgentProviderInfo = {
  id: AgentProvider;
  name: string;
  icon: string;
};

export type AgentSessionInfo = {
  id: string;
  provider: AgentProvider;
  ref: string;
  provider_session_id?: string | null;
  title: string;
  cwd: string;
  cwd_relative?: string | null;
  model?: string | null;
  created_at: number;
  updated_at: number;
  status: AgentStatus;
  exit_code?: number | null;
  event_count: number;
  total_tokens?: number | null;
  queue: CodexQueueItem[];
  raw: Record<string, unknown>;
};

export type AgentSessionSnapshot = AgentSessionInfo & {
  prompts: CodexPrompt[];
  events: CodexEvent[];
};

export type AgentSocketMessage =
  | { type: "snapshot"; session: AgentSessionSnapshot; source?: string }
  | { type: "event"; event: CodexEvent; session: AgentSessionInfo; source?: string }
  | { type: "status"; session: AgentSessionInfo; source?: string }
  | { type: "deleted"; source?: string };
