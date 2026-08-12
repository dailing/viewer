/**
 * Instance-scoped plugin ctx (framework section 8.3).
 *
 * The ctx tracks every subscription it creates; `dispose()` (pane closed,
 * plugin deactivated) unsubscribes them all, so plugins never write cleanup
 * code and never leak (framework section 8.3 rule 6).
 */

import type { FrameHandler } from "@viewer/bus-sdk";

import { bus } from "./bus";

export interface CtxStorage {
  get<T>(key: string, fallback: T): T;
  set(key: string, value: unknown): void;
  remove(key: string): void;
}

export interface PluginCtx {
  /** Plugin or pane scope, e.g. `bus-inspector` or `bus-inspector:main`. */
  readonly scope: string;
  readonly bus: {
    subscribe(pattern: string, handler: FrameHandler): void;
    unsubscribe(pattern: string, handler: FrameHandler): void;
    publish: typeof bus.publish;
    set: typeof bus.set;
    request: typeof bus.request;
    cancel: typeof bus.cancel;
  };
  /** F6: namespaced localStorage. */
  readonly storage: CtxStorage;
  onDispose(callback: () => void): void;
  dispose(): void;
}

function createStorage(scope: string): CtxStorage {
  const prefix = `viewer:${scope}:`;
  return {
    get<T>(key: string, fallback: T): T {
      const raw = localStorage.getItem(prefix + key);
      if (raw === null) return fallback;
      try {
        return JSON.parse(raw) as T;
      } catch {
        return fallback;
      }
    },
    set(key: string, value: unknown): void {
      localStorage.setItem(prefix + key, JSON.stringify(value));
    },
    remove(key: string): void {
      localStorage.removeItem(prefix + key);
    },
  };
}

export function createCtx(scope: string): PluginCtx {
  const subscriptions: Array<{ pattern: string; handler: FrameHandler }> = [];
  const disposeCallbacks: Array<() => void> = [];
  let disposed = false;

  const ctx: PluginCtx = {
    scope,
    bus: {
      subscribe(pattern: string, handler: FrameHandler): void {
        if (disposed) return;
        subscriptions.push({ pattern, handler });
        bus.subscribe(pattern, handler).catch(() => undefined);
      },
      unsubscribe(pattern: string, handler: FrameHandler): void {
        const index = subscriptions.findIndex(
          (entry) => entry.pattern === pattern && entry.handler === handler,
        );
        if (index >= 0) subscriptions.splice(index, 1);
        bus.unsubscribe(pattern, handler).catch(() => undefined);
      },
      publish: bus.publish.bind(bus),
      set: bus.set.bind(bus),
      request: bus.request.bind(bus),
      cancel: bus.cancel.bind(bus),
    },
    storage: createStorage(scope),
    onDispose(callback: () => void): void {
      disposeCallbacks.push(callback);
    },
    dispose(): void {
      if (disposed) return;
      disposed = true;
      for (const { pattern, handler } of subscriptions.splice(0)) {
        bus.unsubscribe(pattern, handler).catch(() => undefined);
      }
      for (const callback of disposeCallbacks.splice(0)) callback();
    },
  };
  return ctx;
}
