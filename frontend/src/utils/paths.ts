export function parentPath(path: string): string {
  return path.includes("/") ? path.split("/").slice(0, -1).join("/") : "";
}

export function fileChangeAffectsPath(eventPath: string, filePath: string): boolean {
  return eventPath === filePath || eventPath === parentPath(filePath);
}
