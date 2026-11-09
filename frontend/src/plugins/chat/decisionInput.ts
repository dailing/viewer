import type { Decision } from "./types";

export interface DecisionResponseIntent {
  decisionId: string;
  response: string;
  expectedVersion: number;
}

/** Freeze all response-routing inputs before the first await. An option click
 * and edited/free text deliberately produce the same RPC shape. */
export function decisionResponseIntent(decision: Pick<Decision, "id" | "version">, draft: string, optionLabel?: string): DecisionResponseIntent | undefined {
  const response = (optionLabel ?? draft).trim();
  if (!response) return undefined;
  return { decisionId: decision.id, response, expectedVersion: decision.version };
}

export function decisionResponsePayload(intent: DecisionResponseIntent): Record<string, unknown> {
  return { decision_id: intent.decisionId, response: intent.response, expected_version: intent.expectedVersion };
}

export function mergeDecision<T extends { id: string }>(items: readonly T[], update: T): T[] {
  const byId = new Map(items.map((item) => [item.id, item]));
  byId.set(update.id, update);
  return [...byId.values()].sort((a, b) => {
    const left = a as T & { created_at?: number };
    const right = b as T & { created_at?: number };
    return (right.created_at ?? 0) - (left.created_at ?? 0) || right.id.localeCompare(left.id);
  });
}
