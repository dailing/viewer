/** uuidv4: secure-context fallback produces real UUIDv4 in any context. */

import { describe, expect, it } from "vitest";

import { uuidv4 } from "../src/client.js";

const UUID_V4_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

describe("uuidv4", () => {
  it("produces UUIDv4 format", () => {
    for (let i = 0; i < 100; i += 1) {
      expect(uuidv4()).toMatch(UUID_V4_RE);
    }
  });

  it("falls back to getRandomValues when randomUUID is unavailable", () => {
    const original = crypto.randomUUID;
    try {
      // Simulate an insecure-context browser (http LAN origin).
      Object.defineProperty(crypto, "randomUUID", { value: undefined, configurable: true });
      expect(uuidv4()).toMatch(UUID_V4_RE);
    } finally {
      Object.defineProperty(crypto, "randomUUID", { value: original, configurable: true });
    }
  });
});
