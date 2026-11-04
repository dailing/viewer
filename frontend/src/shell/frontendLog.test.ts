import { beforeEach, describe, expect, it, vi } from "vitest";
import { bus } from "./bus";
import { FRONTEND_LOG_CHANNEL, reportFrontendLog } from "./frontendLog";

vi.mock("./bus", () => ({ bus: { publish: vi.fn() } }));
const publish = vi.mocked(bus.publish);

beforeEach(() => {
  publish.mockReset();
  publish.mockResolvedValue(undefined);
});

describe("frontend remote logging", () => {
  it("publishes a structured event on the shared bus", () => {
    reportFrontendLog("warn", "chat", "stale input state", { chatId: "c" });
    expect(publish).toHaveBeenCalledWith(FRONTEND_LOG_CHANNEL, {
      level: "warn",
      component: "chat",
      message: "stale input state",
      context: { chatId: "c" },
      ts: expect.any(Number),
    });
  });

  it("does not surface a disconnected-bus rejection to the caller", async () => {
    publish.mockRejectedValue(new Error("not connected"));
    expect(() => reportFrontendLog("info", "shell", "diagnostic")).not.toThrow();
    await Promise.resolve();
  });
});
