import type { AgentEvent, AgentPrompt, AgentQueueItem } from "./agents";

export type HermesStatus = "idle" | "running" | "exited" | "failed";

export type HermesSessionInfo = {
  id: string;
  hermes_session_id?: string | null;
  hermes_run_id?: string | null;
  db_path?: string | null;
  title: string;
  cwd: string;
  cwd_relative?: string | null;
  model?: string | null;
  created_at: number;
  updated_at: number;
  status: HermesStatus;
  exit_code?: number | null;
  event_count: number;
  total_tokens?: number | null;
  queue: AgentQueueItem[];
};

export type HermesSessionSnapshot = HermesSessionInfo & {
  prompts: AgentPrompt[];
  events: AgentEvent[];
};
