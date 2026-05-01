import type { LayoutNode } from "./layout";

export interface WorkspaceSnapshot {
  layout: LayoutNode;
  active_pane_id: string | null;
  current_path: string;
  pinned?: string[] | null;
  updated_at?: number | null;
}

export interface WorkspaceData {
  active_workspace_id: string;
  slots: Record<string, WorkspaceSnapshot>;
}
