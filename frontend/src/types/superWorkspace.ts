import type { AgentProvider } from "./agents";

export type SuperRole = {
  id: string;
  name: string;
  description: string;
  provider: AgentProvider;
  cwd: string;
  model?: string | null;
  session_ref: string;
  created_at: number;
  updated_at: number;
};

export type SuperWorkspaceData = {
  roles: SuperRole[];
};

export type SuperRoleCreate = {
  name: string;
  description?: string;
  provider?: AgentProvider;
  cwd?: string;
  model?: string | null;
};

export type SuperRolePatch = Partial<Omit<SuperRole, "id" | "created_at" | "updated_at">>;

export type SuperDispatchResponse = {
  role_ids: string[];
  rationale: string;
  raw?: Record<string, unknown> | null;
};
