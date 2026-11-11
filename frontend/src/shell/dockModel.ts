import type { DockInstance, DockInstanceGroup, DockProvider, InstanceOpenRequest } from "./definePlugin";

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

export interface DockSection {
  key: string;
  provider: DockProvider;
  entries: DockEntry[];
  group?: DockInstanceGroup;
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

/** Group only entries that explicitly declare a weak group; all other plugin
 * entries retain their original flat Dock presentation and relative order.
 * Providers may declare persistent group headers before any instances exist. */
export function collectDockSections(providers: readonly DockProvider[]): DockSection[] {
  const sections: DockSection[] = [];
  const grouped = new Map<string, DockSection>();
  for (const provider of providers) {
    for (const group of provider.groups ?? []) {
      const key = `${provider.type}:group:${group.id}`;
      if (grouped.has(key)) continue;
      const section: DockSection = { key, provider, entries: [], group };
      grouped.set(key, section);
      sections.push(section);
    }
    for (const entry of collectDockEntries([provider])) {
      const group = entry.instance?.group;
      if (group === undefined) {
        sections.push({ key: entry.key, provider: entry.provider, entries: [entry] });
        continue;
      }
      const key = `${entry.provider.type}:group:${group.id}`;
      let section = grouped.get(key);
      if (section === undefined) {
        section = { key, provider: entry.provider, entries: [], group };
        grouped.set(key, section);
        sections.push(section);
      }
      section.entries.push(entry);
    }
  }
  return sections;
}

export interface DockOpener {
  open(request: InstanceOpenRequest): Promise<unknown> | unknown;
}

/** Run the instance's plugin-owned opener, or use the standard shell adapter. */
export async function openDockEntry(entry: DockEntry, opener: DockOpener): Promise<void> {
  if (entry.instance?.open !== undefined) {
    await entry.instance.open();
    return;
  }
  await opener.open({ paneType: entry.provider.type, instanceId: entry.instance?.id ?? "main" });
}

/** Create when the provider has a factory; otherwise restore singleton main. */
export async function openProvider(provider: DockProvider, opener: DockOpener, groupId?: string): Promise<void> {
  const createdId = groupId !== undefined && provider.createForGroup !== undefined
    ? await provider.createForGroup(groupId)
    : await provider.create?.();
  await opener.open({ paneType: provider.type, instanceId: createdId ?? "main" });
}
