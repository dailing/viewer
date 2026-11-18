import type { BackgroundTurn } from "./types";

export function backgroundSnapshot(values: BackgroundTurn[] | undefined): Map<string, BackgroundTurn> {
  return new Map((values ?? []).filter((value) => value.active && value.turn_id).map((value) => [value.turn_id, value]));
}

export function reduceBackgroundEvent(current: Map<string, BackgroundTurn>, value: BackgroundTurn): Map<string, BackgroundTurn> {
  if (!value.turn_id) {
    // Compatibility with pre-Turn identity release frames: their Task-scoped
    // consumer clears its snapshot rather than leaving a stale badge behind.
    return value.active ? current : new Map();
  }
  const next = new Map(current);
  if (value.active) next.set(value.turn_id, value);
  else next.delete(value.turn_id);
  return next;
}
