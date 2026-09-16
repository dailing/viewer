export type EntryType = "file" | "directory" | "symlink" | "other";

export interface FileEntry {
  name: string;
  path: string;
  type: EntryType;
  size: number | null;
  mtime: number | null;
  mime: string | null;
  is_dir: boolean;
  is_symlink: boolean;
  link_target: string | null;
}

export interface DirectoryListing {
  path: string;
  entries: FileEntry[];
}

export type PreviewKind = "image" | "markdown" | "html" | "text" | "pdf";

const IMAGE_MIME_BY_EXT: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  avif: "image/avif",
  bmp: "image/bmp",
  ico: "image/x-icon",
  svg: "image/svg+xml",
};

const MARKDOWN_EXTS = new Set(["md", "markdown", "mdown", "mkd"]);
const HTML_EXTS = new Set(["html", "htm"]);

export function extensionOf(path: string): string {
  const name = basename(path);
  const dot = name.lastIndexOf(".");
  return dot <= 0 ? "" : name.slice(dot + 1).toLowerCase();
}

export function basename(path: string): string {
  const segments = path.split(/[\\/]/).filter((segment) => segment.length > 0);
  return segments.length === 0 ? path : segments[segments.length - 1];
}

/** Parent directory ("" for relative roots, "/" for filesystem-root children). */
export function dirname(path: string): string {
  const normalized = path.replace(/[\\/]+$/, "");
  const cut = normalized.search(/[\\/][^\\/]+$/);
  if (cut < 0) return "";
  const dir = normalized.slice(0, cut);
  return dir === "" ? "/" : dir;
}

/** Extension-based preview routing; "text" falls back to a binary notice when the read comes back base64. */
export function kindForPath(path: string): PreviewKind {
  const ext = extensionOf(path);
  if (ext in IMAGE_MIME_BY_EXT) return "image";
  if (ext === "pdf") return "pdf";
  if (MARKDOWN_EXTS.has(ext)) return "markdown";
  if (HTML_EXTS.has(ext)) return "html";
  return "text";
}

export function imageMimeFor(path: string): string {
  return IMAGE_MIME_BY_EXT[extensionOf(path)] ?? "application/octet-stream";
}

/** Result of the file:_:pdfpage RPC — one rasterized PDF page as base64 WebP. */
export interface PdfPageResult {
  path: string;
  page: number;
  pages: number;
  mtime: number;
  page_width: number;
  page_height: number;
  /** Actual raster pixels after server-side white-margin trimming. */
  image_width: number;
  image_height: number;
  dpi: number;
  mime: string;
  encoding: "base64";
  content: string;
}
