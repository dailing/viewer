import { describe, expect, it } from "vitest";

import { channelMatches } from "../src/matching.js";

// Same table as the kernel's Go suite (the kernel's own suite).
const MATCH_TABLE: Array<[string, string, boolean]> = [
  ["chat:42:status", "chat:42:status", true],
  ["*:42:status", "chat:42:status", true],
  ["chat:*:status", "chat:42:status", true],
  ["chat:42:*", "chat:42:status", true],
  ["chat:42:status:*", "chat:42:status:detail", true],
  ["chat:42", "chat:42:message", true],
  ["chat", "chat:42:message:detail", true],
  ["chat:42:message:detail", "chat:42:message", false],
  [">", "_inbox:c1:r1", true],
  ["chat:*:status", "chat:42:message", false],
];

describe("channelMatches", () => {
  it.each(MATCH_TABLE)("pattern %s vs channel %s -> %s", (pattern, channel, expected) => {
    expect(channelMatches(pattern, channel)).toBe(expected);
  });
});
