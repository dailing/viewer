export type AgentTaskStatus =
  | "draft"
  | "backlog"
  | "ready"
  | "claimed"
  | "running"
  | "waiting_process"
  | "review"
  | "done"
  | "failed"
  | "blocked"
  | "cancelled";

export type AgentTaskMode = "manual" | "auto";

export type AgentTaskExecution = {
  mode: "agent" | "shell" | "manual";
  instruction: string;
  command?: string | null;
  cwd: string;
  env: Record<string, string>;
  timeout_sec?: number | null;
};

export type AgentTaskRuntime = {
  pid?: number | null;
  process_group_id?: number | null;
  exit_code?: number | null;
  started_at?: number | null;
  ended_at?: number | null;
  heartbeat_at?: number | null;
  attempt: number;
  lease_owner?: string | null;
  lease_expires_at?: number | null;
};

export type AgentTaskArtifact = {
  type: string;
  path: string;
  label?: string | null;
};

export type AgentTaskResult = {
  summary?: string | null;
  decision?: string | null;
  metrics: Record<string, unknown>;
  failure_reason?: string | null;
  next_suggestions: string[];
  user_decision_needed?: string | null;
};

export type AgentTaskPolicy = {
  auto_dispatch?: boolean | null;
  requires_approval: boolean;
  max_depth?: number | null;
  max_children?: number | null;
};

export type AgentTask = {
  id: string;
  user_id: string;
  group_id: string;
  root_id: string;
  parent_id?: string | null;
  title: string;
  description: string;
  status: AgentTaskStatus;
  blocked_reason?: string | null;
  priority: number;
  kind: string;
  workspace: string;
  assigned_agent: string;
  model?: string | null;
  agent_session_id?: string | null;
  depends_on: string[];
  execution: AgentTaskExecution;
  runtime: AgentTaskRuntime;
  artifacts: AgentTaskArtifact[];
  result: AgentTaskResult;
  metadata: Record<string, unknown>;
  policy: AgentTaskPolicy;
  version: number;
  created_by: string;
  created_at: number;
  updated_at: number;
};

export type AgentTaskSettings = {
  user_id: string;
  group_id: string;
  mode: AgentTaskMode;
  default_agent: string;
  default_model?: string | null;
  auto_tick_seconds: number;
  updated_at: number;
};

export type AgentTaskListResponse = {
  tasks: AgentTask[];
  groups: string[];
  settings: AgentTaskSettings;
};

export type AgentTaskContext = {
  task: AgentTask;
  dependencies: AgentTask[];
  children: AgentTask[];
  ancestors: AgentTask[];
  events: AgentTaskEvent[];
};

export type AgentTaskEvent = {
  id: string;
  task_id?: string | null;
  user_id: string;
  group_id: string;
  actor: string;
  agent_session_id?: string | null;
  type: string;
  reason: string;
  before?: unknown;
  after?: unknown;
  created_at: number;
};

export type AgentTaskCreate = Partial<AgentTask> & {
  title: string;
  group_id: string;
};

export type AgentTaskPatch = Partial<AgentTask> & {
  expected_version?: number | null;
  reason?: string;
};

export type AgentTaskDependencyPatch = {
  add?: string[];
  remove?: string[];
  replace?: string[] | null;
  expected_version?: number | null;
  reason?: string;
};
