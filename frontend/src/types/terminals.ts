export type TerminalStatus = "running" | "exited";

export interface TerminalInfo {
  id: string;
  title: string;
  shell: string;
  cwd: string;
  created_at: number;
  status: TerminalStatus;
  exit_code: number | null;
  rows: number;
  cols: number;
  layout_locked: boolean;
}

export interface TerminalSnapshot extends TerminalInfo {
  output: string;
  output_version: number;
}
