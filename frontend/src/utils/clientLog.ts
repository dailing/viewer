type ClientLogLevel = "debug" | "info" | "warning" | "error";

interface ClientLogEntry {
  level: ClientLogLevel;
  message: string;
  source?: string;
  stack?: string;
  url?: string;
  user_agent?: string;
}

function stringify(value: unknown): string {
  if (value instanceof Error) return value.message;
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

function sendClientLog(entry: ClientLogEntry) {
  const payload = JSON.stringify({
    source: "frontend",
    url: window.location.href,
    user_agent: navigator.userAgent,
    ...entry,
  });

  if (navigator.sendBeacon) {
    const blob = new Blob([payload], { type: "application/json" });
    if (navigator.sendBeacon("/api/debug/client-log", blob)) return;
  }

  void fetch("/api/debug/client-log", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: payload,
    keepalive: true,
  }).catch(() => undefined);
}

export function installClientLogging() {
  window.addEventListener("error", (event) => {
    sendClientLog({
      level: "error",
      message: event.message || "Unhandled frontend error",
      stack: event.error instanceof Error ? event.error.stack : undefined,
    });
  });

  window.addEventListener("unhandledrejection", (event) => {
    const reason = event.reason;
    sendClientLog({
      level: "error",
      message: `Unhandled promise rejection: ${stringify(reason)}`,
      stack: reason instanceof Error ? reason.stack : undefined,
    });
  });

  const originalError = console.error.bind(console);
  console.error = (...args: unknown[]) => {
    originalError(...args);
    sendClientLog({
      level: "error",
      message: args.map(stringify).join(" "),
      stack: args.find((arg): arg is Error => arg instanceof Error)?.stack,
    });
  };
}
