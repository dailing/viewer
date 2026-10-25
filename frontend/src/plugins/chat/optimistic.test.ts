import { describe, expect, it } from "vitest";
import { optimisticLinesVisible, resolveOptimisticTurn, settleOptimisticSend } from "./optimistic";

describe("branch-scoped optimistic chat state", () => {
  it("shows pending rows only on their owning views", () => {
    expect(optimisticLinesVisible(["branch-a"], ["branch-a"])).toBe(true);
    expect(optimisticLinesVisible(["branch-a"], ["branch-b"])).toBe(false);
    expect(optimisticLinesVisible(["branch-a", "branch-b"], ["branch-b"])).toBe(true);
    expect(optimisticLinesVisible([""], [""])).toBe(true);
    expect(optimisticLinesVisible(["branch-a"], null)).toBe(true);
  });

  it("does not clear another branch's routed optimistic send", () => {
    const pending = [
      { key: "a", text: "same", sending: false, failed: "", branchIds: ["branch-a"] },
      { key: "b", text: "other", sending: false, failed: "", branchIds: ["branch-b"] },
    ];
    expect(settleOptimisticSend(pending, { text: "same", branchIds: ["branch-b"] })).toBe(pending);
    expect(settleOptimisticSend(pending, { text: "same", branchIds: ["branch-a"] }).map((item) => item.key)).toEqual(["b"]);
  });

  it("accepts an auto-branch echo while its dispatch reply is pending", () => {
    const pending = [{ key: "a", text: "same", sending: true, failed: "", branchIds: [""] }];
    expect(settleOptimisticSend(pending, { text: "same", branchIds: ["new-branch"] })).toEqual([]);
  });

  it("resolves the exact dispatch, never another same-role branch", () => {
    const pending = new Map([
      ["d-a\0role", { dispatchId: "d-a", roleId: "role", branchId: "branch-a" }],
      ["d-b\0role", { dispatchId: "d-b", roleId: "role", branchId: "branch-b" }],
    ]);
    const remaining = resolveOptimisticTurn(pending, "role", "branch-b", "d-a");
    expect([...remaining.keys()]).toEqual(["d-b\0role"]);
    expect(resolveOptimisticTurn(remaining, "role", "branch-a")).toBe(remaining);
    expect(resolveOptimisticTurn(remaining, "role", "branch-b").size).toBe(0);
  });
});
