import { describe, expect, it, vi } from "vitest";
import { ancestorPaths, DirectoryTreeModel } from "./directoryTree";

function listing(path: string) {
  const names: Record<string, string[]> = {
    "/": ["home", "var"],
    "/home": ["user"],
    "/home/user": ["project", "linked"],
  };
  return {
    path,
    entries: (names[path] ?? []).map((name) => ({
      name,
      path: `${path === "/" ? "" : path}/${name}`,
      is_dir: true,
      is_symlink: name === "linked",
    })),
  };
}

describe("DirectoryTreeModel", () => {
  it("loads only expanded ancestors and filters directory symlinks", async () => {
    const request = vi.fn(async (_channel: string, value: any) => listing(value.path));
    const tree = new DirectoryTreeModel();
    expect(request).not.toHaveBeenCalled();
    await tree.reveal({ request }, "/home/user");
    expect(request.mock.calls.map((call) => call[1].path)).toEqual(["/", "/home", "/home/user"]);
    expect(request.mock.calls.every((call) => call[1].dirs_only === true)).toBe(true);
    expect(request.mock.calls.every((call) => call[1].show_hidden === false)).toBe(true);
    expect(tree.children("/").map((node) => node.path)).toEqual(["/home", "/var"]);
    expect(tree.children("/home/user").map((node) => node.path)).toEqual(["/home/user/project"]);
    expect(tree.nodes.get("/home/user")?.expanded).toBe(true);
    expect(tree.nodes.get("/var")?.loaded).toBe(false);
  });

  it("passes show_hidden to directory requests when enabled", async () => {
    const request = vi.fn(async (_channel: string, value: any) => listing(value.path));
    const tree = new DirectoryTreeModel(true);
    await tree.reveal({ request }, "/home/user");
    expect(request).toHaveBeenCalledTimes(3);
    expect(request.mock.calls.every((call) => call[1].show_hidden === true)).toBe(true);
  });

  it("loads additional child pages only on demand", async () => {
    const request = vi.fn(async (_channel: string, value: any) => {
      const offset = value.offset as number;
      const names = offset === 0 ? ["a", "b"] : ["c"];
      return {
        path: "/",
        entries: names.map((name) => ({ name, path: `/${name}`, is_dir: true, is_symlink: false })),
        has_more: offset === 0,
        next_offset: offset + names.length,
      };
    });
    const tree = new DirectoryTreeModel();
    await tree.toggle({ request }, "/");
    expect(tree.children("/").map((node) => node.path)).toEqual(["/a", "/b"]);
    expect(tree.nodes.get("/")?.hasMore).toBe(true);
    await tree.loadMore({ request }, "/");
    expect(tree.children("/").map((node) => node.path)).toEqual(["/a", "/b", "/c"]);
    expect(request.mock.calls.map((call) => call[1].offset)).toEqual([0, 2]);
    expect(tree.nodes.get("/")?.hasMore).toBe(false);
  });

  it("collapses without refetching and invalidates only one subtree", async () => {
    const request = vi.fn(async (_channel: string, value: any) => listing(value.path));
    const tree = new DirectoryTreeModel();
    await tree.reveal({ request }, "/home/user");
    await tree.toggle({ request }, "/home/user");
    expect(tree.nodes.get("/home/user")?.expanded).toBe(false);
    await tree.toggle({ request }, "/home/user");
    expect(request).toHaveBeenCalledTimes(3);
    await tree.invalidate({ request }, "/home");
    expect(tree.nodes.get("/home/user")?.loaded).toBe(false);
    expect(tree.nodes.has("/home/user/project")).toBe(false);
    expect(tree.nodes.has("/var")).toBe(true);
    expect(request).toHaveBeenCalledTimes(4);
  });
});

describe("ancestorPaths", () => {
  it("handles root and deep paths", () => {
    expect(ancestorPaths("/")).toEqual(["/"]);
    expect(ancestorPaths("/a/b/c")).toEqual(["/", "/a", "/a/b", "/a/b/c"]);
  });
});
