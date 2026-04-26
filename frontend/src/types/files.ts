export type EntryType = "file" | "directory" | "symlink" | "other";
export type PreviewType = "image" | "markdown" | "pdf" | "text" | "unsupported";

export interface FileEntry {
  name: string;
  path: string;
  type: EntryType;
  size: number | null;
  mtime: number | null;
  mime: string | null;
  is_dir: boolean;
  is_symlink: boolean;
}

export interface DirectoryListing {
  path: string;
  entries: FileEntry[];
}

export interface FileMeta {
  name: string;
  path: string;
  size: number;
  mtime: number;
  mime: string;
  preview: PreviewType;
  text_too_large: boolean;
}

export interface ViewerConfig {
  pinned: string[];
}

export interface WatchEvent {
  type: string;
  path: string;
  is_dir: boolean;
  mtime: number | null;
}

