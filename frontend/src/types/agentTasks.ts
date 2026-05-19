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
  task_workspace?: string | null;
};

export type AgentTaskArtifact = {
  type: string;
  path: string;
  label?: string | null;
};

export type AgentTaskFile = {
  source: "artifact" | "workspace";
  type: string;
  name: string;
  path: string;
  view_path?: string | null;
  label?: string | null;
  size?: number | null;
  mtime?: number | null;
  is_dir: boolean;
  viewable: boolean;
  unavailable_reason?: "missing" | "outside_served_root" | string | null;
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
  goal?: string;
  plan?: string;
  context?: string;
  constraints_json?: string;
  project_root?: string;
  manager_session_id?: string | null;
  updated_at: number;
};

export type AgentTaskPlan = {
  user_id: string;
  group_id: string;
  goal: string;
  plan: string;
  context: string;
  constraints: string[];
  project_root?: string;
  manager_session_id?: string | null;
  updated_at: number;
};

export type AgentTaskGroup = {
  user_id: string;
  group_id: string;
  task_count: number;
  updated_at: number;
  goal: string;
  project_root?: string;
  manager_session_id?: string | null;
  mode: AgentTaskMode;
};

export type AgentTaskListResponse = {
  tasks: AgentTask[];
  groups: string[];
  settings: AgentTaskSettings;
};

export type AgentTaskContext = {
  task: AgentTask;
  plan: AgentTaskPlan;
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

export type AgentTaskManagerRequest = {
  group_id: string;
  task_id?: string | null;
  prompt: string;
  reason?: string;
  trigger?: string;
  model?: string | null;
};

export type AgentTaskResetAction = "clear" | "retry";

export type AgentTaskResetResponse = {
  action: AgentTaskResetAction;
  affected_task_ids: string[];
  tasks: AgentTask[];
  dispatched?: AgentTask | null;
};
