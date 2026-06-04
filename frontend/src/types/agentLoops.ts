import type { CodexSessionSnapshot } from "./codex";

export type AgentLoopScheduleType = "manual" | "once" | "interval" | "daily" | "multi_daily";
export type AgentLoopSessionPolicy = "new_each_run" | "reuse" | "reuse_until_context" | "reuse_with_limits";

export type AgentLoopSchedule = {
  type: AgentLoopScheduleType;
  at_local?: string | null;
  start_at_local?: string | null;
  every_minutes: number;
  time_local: string;
  times_local: string[];
};

export type AgentLoopRunConfig = {
  max_runs?: number | null;
  max_consecutive_failures?: number | null;
  skip_if_previous_running: boolean;
};

export type AgentLoopSessionConfig = {
  policy: AgentLoopSessionPolicy;
  max_context_percent: number;
  reset_after_runs?: number | null;
  reset_on_failure: boolean;
};

export type AgentLoopStopConfig = {
  final_message_regex?: string | null;
};

export type AgentLoopDefinition = {
  id: string;
  name: string;
  enabled: boolean;
  agent: "codex";
  model?: string | null;
  cwd: string;
  timezone: string;
  schedule: AgentLoopSchedule;
  run: AgentLoopRunConfig;
  session: AgentLoopSessionConfig;
  stop: AgentLoopStopConfig;
  prompt: string;
};

export type AgentLoopRuntime = {
  paused: boolean;
  stopped: boolean;
  stop_reason?: string | null;
  current_session_id?: string | null;
  current_run_id?: string | null;
  current_trigger?: string | null;
  run_count: number;
  session_run_count: number;
  consecutive_failures: number;
  last_run_at?: number | null;
  next_run_at?: number | null;
  last_status?: string | null;
  last_error?: string | null;
};

export type AgentLoopInfo = {
  definition: AgentLoopDefinition;
  runtime: AgentLoopRuntime;
  path: string;
  parse_error?: string | null;
};

export type AgentLoopRunRecord = {
  run_id: string;
  task_id: string;
  task_name: string;
  codex_session_id?: string | null;
  trigger: string;
  model?: string | null;
  cwd: string;
  started_at: number;
  finished_at?: number | null;
  status: string;
  exit_code?: number | null;
  error?: string | null;
  prompt?: string;
  session_snapshot?: CodexSessionSnapshot | null;
};
