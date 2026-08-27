import type { ChatBlock } from "./types";

export interface ToolDetailSection { label: string; content: string }
export interface ToolPresentation {
  icon: string;
  label: string;
  summary: string;
  status: string;
  sections: ToolDetailSection[];
}

const SUMMARY_LIMIT = 220;
const DETAIL_BUDGET = 9000;
const TOOL_KINDS = new Set(["tool_call", "tool_result", "file_change", "command"]);

export function isToolActivity(block: ChatBlock | undefined): boolean {
  return Boolean(block && TOOL_KINDS.has(block.kind));
}

function payloadOf(block: ChatBlock): Record<string, unknown> {
  if (!block.payload) return {};
  try {
    const value = JSON.parse(block.payload) as unknown;
    return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
  } catch {
    return {};
  }
}

function textValue(value: unknown): string {
  if (typeof value === "string") return value.trim();
  if (value === undefined || value === null) return "";
  try { return JSON.stringify(value, null, 2); } catch { return String(value); }
}

function oneLine(value: string): string {
  return value.replace(/\s+/g, " ").trim();
}

function clipped(value: string, limit: number): string {
  const source = value.trim();
  if (source.length <= limit) return source;
  const tail = Math.min(900, Math.floor(limit * 0.22));
  const head = limit - tail;
  return `${source.slice(0, head)}\n\n… ${source.length - limit} characters omitted …\n\n${source.slice(-tail)}`;
}

function summaryClip(value: string): string {
  const source = oneLine(value);
  return source.length <= SUMMARY_LIMIT ? source : `${source.slice(0, SUMMARY_LIMIT - 1)}…`;
}

function pathFromDiff(value: string): string {
  const match = value.match(/^diff --git a\/(.+?) b\/(.+)$/m);
  return match?.[2] ?? "";
}

function stripCommandPrefix(value: string): string {
  return value.replace(/^terminal:\s*/i, "").replace(/^\$\s*/, "").trim();
}

function classify(block: ChatBlock, payload: Record<string, unknown>): { icon: string; label: string } {
  const hint = `${textValue(payload.kind)} ${textValue(payload.name)} ${block.text}`.toLowerCase();
  if (block.kind === "thinking") return { icon: "bi-lightbulb", label: "Reasoning" };
  if (block.kind === "error") return { icon: "bi-exclamation-triangle", label: "Error" };
  if (block.kind === "tool_result") return { icon: "bi-check-circle", label: "Result" };
  if (block.kind === "file_change") return { icon: "bi-pencil-square", label: "Edit" };
  if (block.kind === "command") return { icon: "bi-terminal", label: "Command" };
  if (/(^|\s)(execute|terminal|shell|command)(:|\s|$)/.test(hint)) return { icon: "bi-terminal", label: "Command" };
  if (/(^|\s)(edit|patch|write|replace)(:|\s|$)/.test(hint)) return { icon: "bi-pencil-square", label: "Edit" };
  if (/(^|\s)(read|open file)(:|\s|$)/.test(hint)) return { icon: "bi-file-earmark-text", label: "Read" };
  if (/search|query|web/.test(hint)) return { icon: "bi-search", label: "Search" };
  return { icon: "bi-tools", label: "Tool" };
}

export function presentToolBlock(block: ChatBlock): ToolPresentation {
  const payload = payloadOf(block);
  const classified = classify(block, payload);
  const command = textValue(payload.command) || (classified.label === "Command" ? stripCommandPrefix(textValue(payload.input) || block.text) : "");
  const patch = textValue(payload.patch) || textValue(payload.diff);
  const locations = textValue(payload.locations);
  const path = textValue(payload.path) || (patch ? pathFromDiff(patch) : "") || (classified.label === "Read" ? block.text.replace(/^read:\s*/i, "").trim() : "");
  const output = textValue(payload.output) || (block.kind === "command" || block.kind === "tool_result" ? block.text : "");
  const result = textValue(payload.result);
  const input = textValue(payload.input);
  const args = textValue(payload.arguments);
  const status = textValue(payload.status);

  let summary = command || path;
  if (!summary && block.kind === "file_change") summary = pathFromDiff(block.text) || block.text;
  if (!summary) summary = textValue(payload.name) || block.text || output || result || classified.label;
  summary = summaryClip(stripCommandPrefix(summary));

  const candidates: ToolDetailSection[] = [];
  const add = (label: string, content: string): void => {
    const clean = content.trim();
    if (clean && !candidates.some((item) => item.content === clean)) candidates.push({ label, content: clean });
  };
  add("Command", command);
  add("Path", path);
  add("Locations", locations);
  if (input && input !== command && stripCommandPrefix(input) !== command) add("Input", input);
  add("Arguments", args);
  add("Output", output);
  add("Result", result);
  add("Changes", patch || (block.kind === "file_change" ? block.text : ""));
  const metadata = Object.fromEntries(
    ["cwd", "exitCode", "durationMs"].filter((key) => payload[key] !== undefined).map((key) => [key, payload[key]]),
  );
  add("Metadata", Object.keys(metadata).length ? JSON.stringify(metadata, null, 2) : "");
  if (!candidates.length && block.text) add("Details", block.text);

  let remaining = DETAIL_BUDGET;
  const sections: ToolDetailSection[] = [];
  for (const section of candidates) {
    if (remaining <= 0) break;
    const content = clipped(section.content, remaining);
    sections.push({ ...section, content });
    remaining -= content.length;
  }
  return { ...classified, summary, status, sections };
}
