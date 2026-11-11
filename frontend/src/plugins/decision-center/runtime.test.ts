import { describe, expect, it } from "vitest";
import { registryHasChat } from "./runtime";

describe("Decision Center Chat readiness", () => {
  it("recognizes either retained registry entry shape", () => {
    expect(registryHasChat([{ id: "chat" }])).toBe(true);
    expect(registryHasChat([{ manifest: { id: "chat" } }])).toBe(true);
  });

  it("does not refresh for an absent Chat plugin or malformed mailbox", () => {
    expect(registryHasChat([{ id: "gateway" }, { manifest: { id: "llm" } }])).toBe(false);
    expect(registryHasChat(null)).toBe(false);
    expect(registryHasChat({ id: "chat" })).toBe(false);
  });
});
