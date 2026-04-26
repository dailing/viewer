export type TerminalStatus = "running" | "exited";

export interface TerminalInfo {
  id: string;
  title: string;
  shell: string;
  cwd: string;
  created_at: number;
  status: TerminalStatus;
  exit_code: number | null;
}

export interface TerminalSnapshot extends TerminalInfo {
  output: string;
}
