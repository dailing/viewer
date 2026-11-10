import type { Decision, DecisionOption } from "./types";
export interface DecisionResponseIntent {
  decisionId: string;
  response: string;
  expectedVersion: number;
  optionId?: string;
}
/** Freeze all response-routing inputs before the first await. Option identity
 * is retained so stored control actions cannot be triggered by edited text. */
export function decisionResponseIntent(decision: Pick<Decision, "id" | "version">, draft: string, option?: DecisionOption): DecisionResponseIntent | undefined {
  const response = (option?.label ?? draft).trim();
  if (!response) return undefined;
  return { decisionId: decision.id, response, expectedVersion: decision.version, ...(option ? { optionId: option.id } : {}) };
}
export function decisionResponsePayload(intent: DecisionResponseIntent): Record<string, unknown> {
  return { decision_id: intent.decisionId, response: intent.response, expected_version: intent.expectedVersion, ...(intent.optionId ? { option_id: intent.optionId } : {}) };
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
