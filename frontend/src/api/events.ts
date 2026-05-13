import type { WatchEvent } from "../types/files";
import { currentUserId } from "../utils/userProfile";

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
