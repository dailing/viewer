import { describe, expect, it } from "vitest";
import { decisionResponseIntent, decisionResponsePayload, decisionVoiceContextId, decisionVoiceTextForSend, mergeDecision } from "./decisionInput";
const decision = { id: "d1", version: 3, created_at: 10 };
describe("Decision response input", () => {
  it("uses one frozen payload for option, edited option and free text", () => {
    const option = { id: "option-1", label: "Use option A" };
    for (const [draft, selected, response] of [["", option, "Use option A"], ["Use option A, then test", undefined, "Use option A, then test"], ["  free response  ", undefined, "free response"]] as const) {
      const intent = decisionResponseIntent(decision, draft, selected);
      expect(intent).toEqual({ decisionId: "d1", response, expectedVersion: 3, ...(selected ? { optionId: selected.id } : {}) });
      expect(decisionResponsePayload(intent!)).toEqual({ decision_id: "d1", response, expected_version: 3, ...(selected ? { option_id: selected.id } : {}) });
    }
    expect(decisionResponseIntent(decision, "  ")).toBeUndefined();
  });
  it("retains the stored Task-ending option identity", () => {
    const intent = decisionResponseIntent(decision, "", { id: "option-2", label: "结束当前 Task", action: "end_current_task" });
    expect(decisionResponsePayload(intent!)).toEqual({ decision_id: "d1", response: "结束当前 Task", expected_version: 3, option_id: "option-2" });
  });
  it("binds voice per Decision and sends only final refined voice text", async () => {
    expect(decisionVoiceContextId("d1")).toBe("decision:d1");
    let finishCalls = 0;
    const refined = await decisionVoiceTextForSend("typed", { status: "processing", text: "partial" }, async () => {
      finishCalls += 1;
      return "final refined response";
    });
    expect(refined).toBe("final refined response");
    expect(finishCalls).toBe(1);
    await expect(decisionVoiceTextForSend("typed", { status: "ready", text: "ready voice" }, async () => {
      throw new Error("ready text must not restart voice processing");
    })).resolves.toBe("ready voice");
    await expect(decisionVoiceTextForSend("typed", { status: "idle", text: "" }, async () => {
      throw new Error("typed text must not start voice processing");
    })).resolves.toBe("typed");
  });
  it("merges cross-pane status updates without duplicating the card", () => {
    const merged = mergeDecision([decision], { ...decision, created_at: 11 });
    expect(merged).toHaveLength(1);
    expect(merged[0]?.created_at).toBe(11);
  });
});
