import { describe, expect, it } from "vitest";

import { basename, dirname, kindForPath } from "./fileTypes";

describe("file preview routing", () => {
  it.each([
    ["movie.mp4", "video"],
    ["MOVIE.MP4", "video"],
    ["movie.m4v", "video"],
    ["movie.webm", "video"],
    ["movie.ogg", "video"],
    ["movie.ogv", "video"],
    ["movie.mov", "video"],
    ["movie.mkv", "video"],
    ["photo.png", "image"],
    ["paper.pdf", "pdf"],
    ["notes.md", "markdown"],
    ["movie.avi", "text"],
  ] as const)("routes %s as %s", (path, kind) => {
    expect(kindForPath(path)).toBe(kind);
  });

  it("keeps existing path helpers unchanged", () => {
    expect(basename("/tmp/movie.mp4")).toBe("movie.mp4");
    expect(dirname("/tmp/movie.mp4")).toBe("/tmp");
  });
});
