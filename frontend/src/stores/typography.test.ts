import { describe, expect, it } from "vitest";

import { DEFAULT_CONTENT_SIZE, TYPO_DETAILS } from "./typography";

describe("typography heading scale", () => {
  it("keeps headings close to the content base size", () => {
    expect(TYPO_DETAILS.h1.ratio).toBe(1.1);
    expect(TYPO_DETAILS.h2.ratio).toBe(1.05);
    expect(TYPO_DETAILS.h3.ratio).toBe(1);
    expect(TYPO_DETAILS.h4.ratio).toBeCloseTo(14 / 15);
  });

  it("uses 16.5, 15.75, 15 and 14 px at the default content size", () => {
    for (const [key, expected] of Object.entries({ h1: 16.5, h2: 15.75, h3: 15, h4: 14 })) {
      expect(DEFAULT_CONTENT_SIZE * TYPO_DETAILS[key].ratio).toBeCloseTo(expected);
    }
  });
});
