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
 * entries retain their original flat Dock presentation and relative order. */
export function collectDockSections(providers: readonly DockProvider[]): DockSection[] {
  const sections: DockSection[] = [];
  const grouped = new Map<string, DockSection>();
  for (const entry of collectDockEntries(providers)) {
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
  return sections;
}

export interface DockOpener {
  open(request: InstanceOpenRequest): Promise<unknown> | unknown;
}

/** Create when the provider has a factory; otherwise restore singleton main. */
export async function openProvider(provider: DockProvider, opener: DockOpener, groupId?: string): Promise<void> {
  const createdId = groupId !== undefined && provider.createForGroup !== undefined
    ? await provider.createForGroup(groupId)
    : await provider.create?.();
  await opener.open({ paneType: provider.type, instanceId: createdId ?? "main" });
}
