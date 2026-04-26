import { defineStore } from "pinia";
import { getConfig, getTree, putConfig } from "../api/client";
import type { DirectoryListing, FileEntry, ViewerConfig } from "../types/files";

export const useFilesStore = defineStore("files", {
  state: () => ({
    listings: {} as Record<string, DirectoryListing>,
    currentPath: "",
    expanded: new Set<string>(),
    pinned: [] as string[],
    loading: false,
  }),
  getters: {
    rootEntries(state): FileEntry[] {
      return state.listings[""]?.entries ?? [];
    },
    currentEntries(state): FileEntry[] {
      return state.listings[state.currentPath]?.entries ?? [];
    },
    parentPath(state): string {
      if (!state.currentPath) return "";
      return state.currentPath.includes("/") ? state.currentPath.split("/").slice(0, -1).join("/") : "";
    },
  },
  actions: {
    async loadConfig() {
      const config = await getConfig();
      this.pinned = config.pinned;
    },
    async saveConfig() {
      const config: ViewerConfig = { pinned: this.pinned };
      const saved = await putConfig(config);
      this.pinned = saved.pinned;
    },
    async loadDirectory(path = "") {
      this.loading = true;
      try {
        this.listings[path] = await getTree(path);
      } finally {
        this.loading = false;
      }
    },
    async enterDirectory(path: string) {
      await this.loadDirectory(path);
      this.currentPath = path;
    },
    async enterParentDirectory() {
      await this.enterDirectory(this.parentPath);
    },
    async toggleDirectory(path: string) {
      if (this.expanded.has(path)) {
        this.expanded.delete(path);
        return;
      }
      this.expanded.add(path);
      await this.loadDirectory(path);
    },
    async refreshAffected(path: string, isDir: boolean) {
      if (isDir && this.expanded.has(path)) await this.loadDirectory(path);
      const parent = path.includes("/") ? path.split("/").slice(0, -1).join("/") : "";
      if (this.listings[parent]) await this.loadDirectory(parent);
    },
    async togglePin(path: string) {
      if (this.pinned.includes(path)) {
        this.pinned = this.pinned.filter((item) => item !== path);
      } else {
        this.pinned = [path, ...this.pinned];
      }
      await this.saveConfig();
    },
  },
});
