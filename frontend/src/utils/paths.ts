export function parentPath(path: string): string {
  if (path === "/") return "";
  if (path.startsWith("/")) {
    const trimmed = path.replace(/\/+$/, "");
    const index = trimmed.lastIndexOf("/");
    if (index <= 0) return "/";
    return trimmed.slice(0, index);
  }
  return path.includes("/") ? path.split("/").slice(0, -1).join("/") : "";
}

export function fileChangeAffectsPath(eventPath: string, filePath: string): boolean {
  return eventPath === filePath || eventPath === parentPath(filePath);
}
