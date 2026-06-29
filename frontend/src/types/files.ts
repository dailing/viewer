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

export interface TextLineWindow {
  path: string;
  size: number;
  mtime: number;
  total_lines: number;
  start_line: number;
  lines: string[];
  truncated_start: boolean;
  truncated_end: boolean;
}

export interface AppearanceConfig {
  navbar_size: number;
  color_theme: "light" | "dark";
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
}

export interface VoiceConfig {
  enabled: boolean;
  available_models: string[];
  model: string;
  available_languages: string[];
  language: string;
  translation_enabled: boolean;
  available_target_languages: string[];
  target_language: string;
}

export interface SuperWorkspaceDispatchProfile {
  id: string;
  name: string;
  api_url: string;
  model: string;
  api_key: string;
}

export interface SuperWorkspaceConfig {
  hindsight_retain_enabled: boolean;
  hindsight_api_url: string;
  hindsight_bank_prefix: string;
  chat_history_bootstrap_enabled: boolean;
  chat_history_bootstrap_tokens: number;
  active_dispatch_profile_id: string;
  dispatch_history_word_budget: number;
  dispatch_profiles: SuperWorkspaceDispatchProfile[];
}

export interface UserProfile {
  id: string;
  name: string;
  home: string;
  home_path?: string;
}

export interface ViewerConfig {
  appearance: AppearanceConfig;
  markdown: MarkdownConfig;
  codex?: CodexConfig;
  voice?: VoiceConfig;
  super_workspace?: SuperWorkspaceConfig;
  users?: UserProfile[];
  default_user?: string;
}

export interface WatchEvent {
  type: string;
  path: string;
  is_dir: boolean;
  mtime: number | null;
}
