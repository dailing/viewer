export type GitDiffFile = {
  path: string;
  status: string;
  added: number | null;
  deleted: number | null;
  is_binary: boolean;
};

export type GitStatus = {
  files: GitDiffFile[];
};

export type GitDiffText = {
  path: string;
  diff: string;
  is_binary: boolean;
};
