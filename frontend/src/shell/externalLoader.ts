/** Mailbox entry published by the gateway asset store. */
export interface ExternalAssetEntry {
  url: string;
  entry: string;
  hash: string;
  files?: string[];
  manifest?: { name?: string; icon?: string; description?: string };
}

interface LoadedEntry<T> {
  hash: string;
  handle: T;
}

interface ExternalPluginLoaderOptions<T> {
  load: (id: string, entry: ExternalAssetEntry) => Promise<T>;
  unload: (handle: T) => void;
  isStatic?: (id: string) => boolean;
  onError?: (id: string, entry: ExternalAssetEntry, error: unknown) => void;
  retryMs?: number;
  now?: () => number;
}

/**
 * Reconcile the intersection of live backends and published frontend assets.
 * Mailbox updates and cooldown retries share one promise chain, so an async
 * import can never double-register a plugin. A failed hash is retried by a
 * real timer even when no further mailbox frame arrives.
 */
export class ExternalPluginLoader<T> {
  private assets: Record<string, ExternalAssetEntry> = {};
  private connected = new Set<string>();
  private readonly loaded = new Map<string, LoadedEntry<T>>();
  private readonly failures = new Map<string, { hash: string; at: number }>();
  private chain: Promise<void> = Promise.resolve();
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private retryDeadline: number | null = null;
  private readonly retryMs: number;
  private readonly now: () => number;

  constructor(private readonly options: ExternalPluginLoaderOptions<T>) {
    this.retryMs = options.retryMs ?? 30_000;
    this.now = options.now ?? Date.now;
  }

  setAssets(assets: Record<string, ExternalAssetEntry>): void {
    this.assets = assets;
    this.schedule();
  }

  setConnected(ids: Set<string>): void {
    this.connected = ids;
    this.schedule();
  }

  /** Wait for the currently scheduled reconcile (used by tests and shutdown). */
  settled(): Promise<void> {
    return this.chain;
  }

  private schedule(): void {
    this.chain = this.chain.then(() => this.reconcile()).catch((error: unknown) => {
      console.error("external plugin reconcile failed", error);
    });
  }

  private clearRetry(): void {
    if (this.retryTimer !== null) clearTimeout(this.retryTimer);
    this.retryTimer = null;
    this.retryDeadline = null;
  }

  private armRetry(deadline: number): void {
    if (this.retryDeadline !== null && this.retryDeadline <= deadline) return;
    this.clearRetry();
    this.retryDeadline = deadline;
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      this.retryDeadline = null;
      this.schedule();
    }, Math.max(0, deadline - this.now()));
  }

  private async reconcile(): Promise<void> {
    this.clearRetry();

    for (const [id, current] of [...this.loaded]) {
      if (!(id in this.assets) || !this.connected.has(id)) {
        this.loaded.delete(id);
        this.failures.delete(id);
        this.options.unload(current.handle);
      }
    }

    for (const [id, entry] of Object.entries(this.assets)) {
      if (this.options.isStatic?.(id) === true || !this.connected.has(id)) continue;
      const current = this.loaded.get(id);
      if (current?.hash === entry.hash) continue;

      const failed = this.failures.get(id);
      if (failed?.hash === entry.hash) {
        const deadline = failed.at + this.retryMs;
        if (this.now() < deadline) {
          this.armRetry(deadline);
          continue;
        }
      }

      if (current !== undefined) {
        // A changed content hash is a new module URL. Unload the old
        // generation before registering the replacement's contributions.
        this.loaded.delete(id);
        this.options.unload(current.handle);
      }

      try {
        const handle = await this.options.load(id, entry);
        this.loaded.set(id, { hash: entry.hash, handle });
        this.failures.delete(id);
      } catch (error) {
        const at = this.now();
        this.failures.set(id, { hash: entry.hash, at });
        this.armRetry(at + this.retryMs);
        this.options.onError?.(id, entry, error);
      }
    }
  }
}
