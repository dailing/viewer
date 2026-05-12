import type { AgentEvent, AgentFileChange, AgentPrompt, AgentQueueItem } from "./agents";

export type CodexStatus = "idle" | "running" | "exited" | "failed";

export type CodexSessionInfo = {
  id: string;
  codex_session_id?: string | null;
  rollout_path?: string | null;
  title: string;
  cwd: string;
  cwd_relative?: string | null;
  model?: string | null;
  created_at: number;
  updated_at: number;
  status: CodexStatus;
  exit_code?: number | null;
  pid?: number | null;
  codex_pid?: number | null;
  run_id?: string | null;
  run_started_at?: number | null;
  event_count: number;
  model_context_window?: number | null;
  context_used_percent?: number | null;
  total_tokens?: number | null;
  queue: AgentQueueItem[];
};

export type CodexPrompt = AgentPrompt;
export type CodexEvent = AgentEvent;

export type CodexSessionSnapshot = CodexSessionInfo & {
  prompts: AgentPrompt[];
  events: AgentEvent[];
};

export type CodexFileChange = AgentFileChange;
export type CodexQueueItem = AgentQueueItem;

export type CodexCliStatus = {
  available: boolean;
  session_id?: string | null;
  rollout_path?: string | null;
  updated_at?: number | null;
  cwd?: string | null;
  model?: string | null;
  model_context_window?: number | null;
  context_used_percent?: number | null;
  total_tokens?: number | null;
  plan_type?: string | null;
  primary_used_percent?: number | null;
  primary_remaining_percent?: number | null;
  primary_window_minutes?: number | null;
  secondary_used_percent?: number | null;
  secondary_remaining_percent?: number | null;
  secondary_window_minutes?: number | null;
  selected_model?: string | null;
};

export type CodexModelOptions = {
  selected_model: string;
  available_models: string[];
  source: string;
};
