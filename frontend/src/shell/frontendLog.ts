import { bus } from "./bus";

export const FRONTEND_LOG_CHANNEL = "frontend:_:log";
export type FrontendLogLevel = "debug" | "info" | "warn" | "error";

/** Best-effort remote copy of a frontend diagnostic. The browser console and
 * the operation that produced the diagnostic must not depend on bus health. */
export function reportFrontendLog(
  level: FrontendLogLevel,
  component: string,
  message: string,
  context: Record<string, unknown> = {},
): void {
  void bus.publish(FRONTEND_LOG_CHANNEL, {
    level,
    component,
    message,
    context,
    ts: Date.now(),
  }).catch(() => undefined);
}
