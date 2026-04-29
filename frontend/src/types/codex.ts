export type CodexStatus = "idle" | "running" | "exited" | "failed";

export type CodexSessionInfo = {
  id: string;
  codex_session_id?: string | null;
  title: string;
  cwd: string;
  created_at: number;
  updated_at: number;
  status: CodexStatus;
  exit_code?: number | null;
  event_count: number;
};

export type CodexPrompt = {
  text: string;
  created_at: number;
};

export type CodexEvent = {
  index: number;
  received_at: number;
  raw: Record<string, unknown>;
};

export type CodexSessionSnapshot = CodexSessionInfo & {
  prompts: CodexPrompt[];
  events: CodexEvent[];
};
