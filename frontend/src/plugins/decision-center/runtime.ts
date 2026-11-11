export interface PluginRegistryEntry {
  id?: string;
  manifest?: { id?: string };
}

/** Retained plugin registry tells Decision views when Chat RPC is available. */
export function registryHasChat(value: unknown): boolean {
  return Array.isArray(value) && (value as PluginRegistryEntry[]).some(
    (entry) => entry?.id === "chat" || entry?.manifest?.id === "chat",
  );
}
