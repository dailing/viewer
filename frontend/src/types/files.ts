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
  link_target: string | null;
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
  color_theme: "system" | "light" | "dark";
  density: "compact" | "comfortable";
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
  strong: MarkdownElementStyle;
  code: MarkdownElementStyle;
  code_background: string;
  link_color: string;
  border_color: string;
  syntax: MarkdownSyntaxStyle;
}

export interface MarkdownConfig {
  active_theme: string;
  follow_app_theme: boolean;
  themes: MarkdownTheme[];
}

export interface VoiceConfig {
  enabled: boolean;
  language_model_refine: boolean;
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

export interface ProviderContextLimitConfig {
  context_recycle_percent: number;
  context_recycle_tokens: number | null;
}

export interface RoutingCandidateConfig {
  id: string;
  name: string;
  target_id: string;
  agent_id: string;
  provider_id: string;
  model_id: string;
  selection_id: string;
  enabled: boolean;
  parameters: Record<string, unknown>;
}

export interface RoutingPolicyConfig {
  id: string;
  name: string;
  description: string;
  enabled: boolean;
  auto_failover: boolean;
  max_attempts: number;
  cooldown_seconds: number;
  candidates: RoutingCandidateConfig[];
}

export interface SuperWorkspaceConfig {
  routing_schema_version: number;
  hindsight_retain_enabled: boolean;
  hindsight_api_url: string;
  hindsight_bank_prefix: string;
  chat_virtual_space_enabled: boolean;
  chat_history_bootstrap_enabled: boolean;
  chat_history_bootstrap_tokens: number;
  active_dispatch_profile_id: string;
  dispatch_history_word_budget: number;
  provider_context_limits: Record<string, ProviderContextLimitConfig>;
  default_routing_policy_id: string;
  routing_policies: RoutingPolicyConfig[];
  dispatch_prompt_template: string;
  dispatch_profiles: SuperWorkspaceDispatchProfile[];
}

export interface ViewerConfig {
  appearance: AppearanceConfig;
  markdown: MarkdownConfig;
  voice?: VoiceConfig;
  super_workspace?: SuperWorkspaceConfig;
}

export interface WatchEvent {
  type: string;
  path: string;
  is_dir: boolean;
  mtime: number | null;
}
