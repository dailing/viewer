import type { LayoutNode } from "./layout";

export interface WorkspaceSnapshot {
  layout: LayoutNode;
  active_pane_id: string | null;
  current_path: string;
  pinned?: string[] | null;
  agent_session_ids?: string[];
  visit_times?: Record<string, number>;
  updated_at?: number | null;
}

export interface WorkspaceSummary {
  id: string;
  name: string;
  created_at: number;
  updated_at: number;
}

export interface WorkspaceData {
  active_workspace_id: string;
  count: number;
  workspaces: WorkspaceSummary[];
  slots: Record<string, WorkspaceSnapshot>;
}
