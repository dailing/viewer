import { describe, expect, it } from "vitest";
import { decisionResponseIntent, decisionResponsePayload, mergeDecision } from "./decisionInput";

const decision = { id: "d1", version: 3, created_at: 10 };

describe("Decision response input", () => {
  it("uses one frozen payload for option, edited option and free text", () => {
    for (const [draft, option, response] of [["", "Use option A", "Use option A"], ["Use option A, then test", undefined, "Use option A, then test"], ["  free response  ", undefined, "free response"]] as const) {
      const intent = decisionResponseIntent(decision, draft, option);
      expect(intent).toEqual({ decisionId: "d1", response, expectedVersion: 3 });
      expect(decisionResponsePayload(intent!)).toEqual({ decision_id: "d1", response, expected_version: 3 });
    }
    expect(decisionResponseIntent(decision, "  ")).toBeUndefined();
  });

  it("merges cross-pane status updates without duplicating the card", () => {
    const merged = mergeDecision([decision], { ...decision, created_at: 11 });
    expect(merged).toHaveLength(1);
    expect(merged[0]?.created_at).toBe(11);
  });
});
