export interface LoopRun {
  id: string; chat_id: string; role_id: string; role_name: string; branch_id: string;
  goal: string; criteria: string; progress_path: string; state: string; reason: string;
  max_iterations: number; iteration: number; duration_seconds: number; turn_timeout_seconds: number;
  deadline: number; feedback: string; revision: number; created_at: number;
}
export interface LoopIteration {
  id: string; number: number; state: string; dispatch_id: string; turn_id: string;
  started_at: number; ended_at: number; verdict: string; feedback: string;
  checkpoint_error: string; stop_reason: string;
}
