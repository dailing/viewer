import { describe, expect, it } from "vitest";
import { draftRequestCurrent } from "./draftSync";

describe("draft request guards", () => {
  const request = { branchId: "a", generation: 3, revision: 7 };

  it("accepts only the response for the unchanged line and edit revision", () => {
    expect(draftRequestCurrent(request, { ...request })).toBe(true);
    expect(draftRequestCurrent(request, { ...request, revision: 8 })).toBe(false);
  });

  it("rejects late responses after a branch switch or another draft load", () => {
    expect(draftRequestCurrent(request, { ...request, branchId: "b" })).toBe(false);
    expect(draftRequestCurrent(request, { ...request, generation: 4 })).toBe(false);
  });
});
