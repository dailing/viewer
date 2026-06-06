import type { WatchEvent } from "../types/files";
import { currentUserId } from "../utils/userProfile";

export type SuperWorkspaceEvent = {
  type?: string;
  user_id?: string;
  run_id?: string;
  status?: string;
  updated_at?: number;
  [key: string]: unknown;
};

export function connectEvents(onChange: (event: WatchEvent) => void, onState?: (state: string) => void): EventSource {
  const user = currentUserId();
  const source = new EventSource(user ? `/api/events?user=${encodeURIComponent(user)}` : "/api/events");
  source.addEventListener("open", () => onState?.("connected"));
  source.addEventListener("error", () => onState?.("reconnecting"));
  source.addEventListener("file-change", (message) => {
    onChange(JSON.parse((message as MessageEvent).data) as WatchEvent);
  });
  return source;
}

export function connectSuperWorkspaceEvents(onEvent: (event: SuperWorkspaceEvent) => void, onState?: (state: string) => void): EventSource {
  const user = currentUserId();
  const source = new EventSource(user ? `/api/super-workspace/events?user=${encodeURIComponent(user)}` : "/api/super-workspace/events");
  source.addEventListener("open", () => onState?.("connected"));
  source.addEventListener("error", () => onState?.("reconnecting"));
  source.addEventListener("super-workspace", (message) => {
    onEvent(JSON.parse((message as MessageEvent).data) as SuperWorkspaceEvent);
  });
  return source;
}
