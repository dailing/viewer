import { reactive } from "vue";
import type { DirectoryListing } from "../../services/fileTypes";
import { basename, dirname } from "../../services/fileTypes";

export interface DirectoryTreeNode {
  path: string;
  name: string;
  expanded: boolean;
  loading: boolean;
  loaded: boolean;
  hasMore: boolean;
  nextOffset: number;
  children: string[];
  error?: string;
}
interface TreeBus {
  request(channel: string, value: unknown): Promise<unknown>;
}

export function ancestorPaths(path: string): string[] {
  if (path === "" || path === "/") return ["/"];
  const parts = path.split("/").filter(Boolean);
  const result = ["/"];
  let current = "";
  for (const part of parts) {
    current += `/${part}`;
    result.push(current);
  }
  return result;
}

/** Lazy directory-only tree; no recursive request happens before expansion. */
export class DirectoryTreeModel {
  readonly nodes = reactive(new Map<string, DirectoryTreeNode>());
  private pending = new Map<string, Promise<void>>();

  constructor(private readonly showHidden = false) {
    this.nodes.set("/", this.node("/", "/"));
  }

  children(path: string): DirectoryTreeNode[] {
    const node = this.nodes.get(path);
    if (node === undefined) return [];
    return node.children.map((child) => this.nodes.get(child)).filter((child): child is DirectoryTreeNode => child !== undefined);
  }

  async toggle(bus: TreeBus, path: string): Promise<void> {
    const node = this.nodes.get(path);
    if (node === undefined) return;
    if (node.expanded) {
      node.expanded = false;
      return;
    }
    await this.load(bus, path);
  }

  async loadMore(bus: TreeBus, path: string): Promise<void> {
    const node = this.nodes.get(path);
    if (node === undefined || node.loading || !node.hasMore) return;
    const request = this.fetch(bus, node, node.nextOffset).finally(() => this.pending.delete(path));
    this.pending.set(path, request);
    return request;
  }

  async reveal(bus: TreeBus, path: string): Promise<void> {
    for (const ancestor of ancestorPaths(path)) await this.load(bus, ancestor);
  }

  async invalidate(bus: TreeBus, path: string): Promise<void> {
    const node = this.nodes.get(path);
    if (node === undefined) return;
    const reopen = node.expanded;
    for (const key of this.nodes.keys()) {
      if (key !== path && (path === "/" || key.startsWith(`${path}/`))) this.nodes.delete(key);
    }
    node.children = [];
    node.loaded = false;
    node.hasMore = false;
    node.nextOffset = 0;
    node.expanded = false;
    if (reopen) await this.load(bus, path);
  }

  private async load(bus: TreeBus, path: string): Promise<void> {
    const node = this.nodes.get(path);
    if (node === undefined || node.loading || node.loaded && node.expanded) return;
    node.expanded = true;
    if (node.loaded) return;
    const existing = this.pending.get(path);
    if (existing !== undefined) return existing;
    const request = this.fetch(bus, node, 0).finally(() => this.pending.delete(path));
    this.pending.set(path, request);
    return request;
  }

  private async fetch(bus: TreeBus, node: DirectoryTreeNode, offset: number): Promise<void> {
    node.loading = true;
    node.error = undefined;
    try {
      const listing = await bus.request("file:_:list", {
        path: node.path,
        dirs_only: true,
        show_hidden: this.showHidden,
        offset,
        limit: 100,
      }) as DirectoryListing;
      const children = listing.entries
        .filter((entry) => entry.is_dir && !entry.is_symlink)
        .map((entry) => {
          const existing = this.nodes.get(entry.path);
          if (existing !== undefined) return existing.path;
          this.nodes.set(entry.path, this.node(entry.path, entry.name));
          return entry.path;
        });
      node.children = offset === 0 ? children : [...new Set([...node.children, ...children])];
      node.hasMore = listing.has_more === true;
      node.nextOffset = listing.next_offset ?? offset + listing.entries.length;
      node.loaded = true;
    } catch (cause) {
      node.error = cause instanceof Error ? cause.message : "无法加载文件夹";
    } finally {
      node.loading = false;
    }
  }

  private node(path: string, name: string): DirectoryTreeNode {
    return {
      path,
      name: path === "/" ? "/" : name || basename(path),
      expanded: false,
      loading: false,
      loaded: false,
      hasMore: false,
      nextOffset: 0,
      children: [],
    };
  }
}

export function parentPath(path: string): string {
  return dirname(path) || "/";
}
