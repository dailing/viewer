import { describe, expect, it } from "vitest";
import { drainDeferredTurn, liveTurnVisibility, stampLiveTurnLine } from "./liveAttribution";

describe("live turn attribution", () => {
  it("defers an unknown turn in a filtered view instead of leaking it into every branch", () => {
    const lines = new Map<string, string>();
    expect(liveTurnVisibility(lines, "turn-new", ["result"])).toBe("deferred");
    expect(liveTurnVisibility(lines, "turn-new", null)).toBe("visible");
  });

  it("distinguishes mainline, another branch, and a selected branch", () => {
    const lines = new Map([["turn-a", "branch20"], ["turn-b", ""]]);
    expect(liveTurnVisibility(lines, "turn-a", ["result"])).toBe("hidden");
    expect(liveTurnVisibility(lines, "turn-a", ["branch20"])).toBe("visible");
    expect(liveTurnVisibility(lines, "turn-b", [""])).toBe("visible");
    expect(liveTurnVisibility(lines, "turn-b", ["branch20"])).toBe("hidden");
  });

  it("drops deferred branch20 frames when their attribution arrives in the result view", () => {
    const deferred = new Map([
      ["block-a", { turn_id: "turn-a", text: "branch20 command" }],
      ["block-b", { turn_id: "turn-b", text: "unrelated" }],
    ]);
    expect(drainDeferredTurn(deferred, "turn-a", false)).toEqual([]);
    expect([...deferred.entries()]).toEqual([["block-b", { turn_id: "turn-b", text: "unrelated" }]]);
  });

  it("releases deferred frames once their own branch is confirmed", () => {
    const block = { turn_id: "turn-a", text: "branch20 command" };
    const deferred = new Map([["block-a", block]]);
    expect(drainDeferredTurn(deferred, "turn-a", true)).toEqual([block]);
    expect(deferred.size).toBe(0);
  });

  it("does not let a phase without branch_id erase a known attribution", () => {
    const lines = new Map<string, string>();
    expect(stampLiveTurnLine(lines, "turn-a", "branch20")).toBe(true);
    expect(stampLiveTurnLine(lines, "turn-a", undefined)).toBe(false);
    expect(lines.get("turn-a")).toBe("branch20");
    expect(stampLiveTurnLine(lines, "turn-b", "")).toBe(true);
    expect(lines.get("turn-b")).toBe("");
  });
});
