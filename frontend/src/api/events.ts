import type { WatchEvent } from "../types/files";

export function connectEvents(onChange: (event: WatchEvent) => void, onState?: (state: string) => void): EventSource {
  const source = new EventSource("/api/events");
  source.addEventListener("open", () => onState?.("connected"));
  source.addEventListener("error", () => onState?.("reconnecting"));
  source.addEventListener("file-change", (message) => {
    onChange(JSON.parse((message as MessageEvent).data) as WatchEvent);
  });
  return source;
}

