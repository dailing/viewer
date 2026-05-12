import type { LayoutNode } from "./layout";

export interface WorkspaceSnapshot {
  layout: LayoutNode;
  active_pane_id: string | null;
  current_path: string;
  pinned?: string[] | null;
  agent_session_ids?: string[];
  codex_session_ids?: string[];
  hermes_session_ids?: string[];
  visit_times?: Record<string, number>;
  updated_at?: number | null;
}

export interface WorkspaceData {
  active_workspace_id: string;
  count: number;
  slots: Record<string, WorkspaceSnapshot>;
}
