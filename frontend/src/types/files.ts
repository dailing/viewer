export type EntryType = "file" | "directory" | "symlink" | "other";
export type PreviewType = "image" | "markdown" | "html" | "pdf" | "text" | "unsupported";

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
  content_hash: string;
  mime: string;
  preview: PreviewType;
  text_too_large: boolean;
}

export interface AppearanceConfig {
  navbar_size: number;
}

export interface MarkdownElementStyle {
  font_size: number | null;
  color: string | null;
  font_weight: string | null;
  line_height: number | null;
}

export interface MarkdownSyntaxStyle {
  background: string;
  text: string;
  keyword: string;
  string: string;
  number: string;
  title: string;
  comment: string;
  meta: string;
}

export interface MarkdownTheme {
  name: string;
  body: MarkdownElementStyle;
  h1: MarkdownElementStyle;
  h2: MarkdownElementStyle;
  h3: MarkdownElementStyle;
  h4: MarkdownElementStyle;
  paragraph: MarkdownElementStyle;
  code: MarkdownElementStyle;
  code_background: string;
  link_color: string;
  border_color: string;
  syntax: MarkdownSyntaxStyle;
}

export interface MarkdownConfig {
  active_theme: string;
  themes: MarkdownTheme[];
}

export interface CodexConfig {
  available_models: string[];
  default_model: string;
  proxy: string;
  muted_message_alpha: number;
  auto_commit_prompt: string;
}

export interface WorkspaceConfig {
  count: number;
  heat_interval_seconds: number;
  heat_step_percent: number;
}

export interface UserProfile {
  id: string;
  name: string;
  home: string;
}

export interface ViewerConfig {
  appearance: AppearanceConfig;
  markdown: MarkdownConfig;
  codex?: CodexConfig;
  workspace?: WorkspaceConfig;
  users?: UserProfile[];
  default_user?: string;
}

export interface WatchEvent {
  type: string;
  path: string;
  is_dir: boolean;
  mtime: number | null;
}
