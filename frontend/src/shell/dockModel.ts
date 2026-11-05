import type { DockInstance, DockProvider, InstanceOpenRequest } from "./definePlugin";

export interface DockEntry {
  key: string;
  uid: string;
  icon: string;
  label: string;
  displayLabel: string;
  state?: string;
  provider: DockProvider;
  instance?: DockInstance;
}

function chatDisplayLabel(label: string): string {
  const separator = " · ";
  const separatorIndex = label.lastIndexOf(separator);
  if (separatorIndex < 0) return label;

  const root = label.slice(separatorIndex + separator.length);
  const withoutTrailingSeparators = root.replace(/[\\/]+$/, "");
  const basename = withoutTrailingSeparators.split(/[\\/]/).pop() || root;
  return `${label.slice(0, separatorIndex)}${separator}${basename}`;
}

function instanceDisplayLabel(provider: DockProvider, instance: DockInstance): string {
  return provider.type === "chat" ? chatDisplayLabel(instance.label) : instance.label;
}

/** Only explicit instances dock; singleton tools live exclusively in "+". */
export function collectDockEntries(providers: readonly DockProvider[]): DockEntry[] {
  const result: DockEntry[] = [];
  for (const provider of providers) {
    for (const instance of provider.instances) {
      const uid = `${provider.type}:${instance.id}`;
      result.push({
        key: uid,
        uid,
        icon: instance.icon ?? provider.icon,
        label: instance.label,
        displayLabel: instanceDisplayLabel(provider, instance),
        state: instance.state,
        provider,
        instance,
      });
    }
  }
  return result;
}

export interface DockOpener {
  open(request: InstanceOpenRequest): Promise<unknown> | unknown;
}

/** Create when the provider has a factory; otherwise restore singleton main. */
export async function openProvider(provider: DockProvider, opener: DockOpener): Promise<void> {
  const createdId = await provider.create?.();
  await opener.open({ paneType: provider.type, instanceId: createdId ?? "main" });
}
