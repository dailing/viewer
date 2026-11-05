/**
 * Pure nbformat → view-model parsing for NotebookPreview (no Vue, no DOM):
 * unit-testable in plain Node. Rendering (markdown-it/hljs) happens in the
 * component on top of this model.
 *
 * Outputs: stream text, base64 images (SVG wrapped as a data-URL <img> so
 * scripts never execute), sandboxed text/html (no JS — interactive outputs
 * like plotly fall back to whatever static content they carry),
 * pretty-printed JSON, and error tracebacks. All text goes through the full
 * ANSI renderer in ansi.ts (SGR colours/styles → spans, carriage-return
 * line overwrite for progress bars). Text outputs cap at 64K characters
 * with a truncation notice.
 */

import { ansiToHtml } from "./ansi";

const TEXT_OUTPUT_CAP = 64 * 1024;

type NbSource = string | string[];

interface NbOutput {
  output_type?: string;
  name?: string;
  text?: NbSource;
  data?: Record<string, unknown>;
  ename?: string;
  evalue?: string;
  traceback?: string[];
}

interface NbCell {
  cell_type?: string;
  source?: NbSource;
  execution_count?: number | null;
  outputs?: NbOutput[];
}

interface NbDocument {
  cells?: NbCell[];
  metadata?: { language_info?: { name?: string } };
}

export type NotebookOutput =
  | { kind: "text"; html: string; stderr: boolean; truncated: boolean }
  | { kind: "html"; html: string }
  | { kind: "image"; src: string; alt: string }
  | { kind: "error"; ename: string; evalue: string; traceHtml: string };

export interface NotebookCell {
  type: "markdown" | "code" | "raw";
  source: string;
  execCount: number | null;
  outputs: NotebookOutput[];
}

export interface NotebookModel {
  language: string;
  cells: NotebookCell[];
}

function sourceText(value: unknown): string {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) return value.filter((line) => typeof line === "string").join("");
  return "";
}

function capText(text: string): { text: string; truncated: boolean } {
  if (text.length <= TEXT_OUTPUT_CAP) return { text, truncated: false };
  return { text: text.slice(0, TEXT_OUTPUT_CAP), truncated: true };
}

function textOutput(text: string, stderr: boolean): NotebookOutput | null {
  const capped = capText(text);
  if (capped.text === "") return null;
  // Cap raw, render after: an unterminated SGR at the cut is tolerated.
  return { kind: "text", html: ansiToHtml(capped.text), stderr, truncated: capped.truncated };
}

/** Notebook base64 payloads carry embedded newlines. */
function imageDataUrl(mime: string, payload: unknown): string | null {
  if (typeof payload !== "string") return null;
  return `data:${mime};base64,${payload.replace(/\s+/g, "")}`;
}

/** SVG is wrapped in an <img> data URL so its scripts never execute. */
function svgDataUrl(payload: unknown): string | null {
  const svg = sourceText(payload).trim();
  if (svg === "") return null;
  const bytes = new TextEncoder().encode(svg);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return `data:image/svg+xml;base64,${btoa(binary)}`;
}

const RASTER_MIMES = ["image/png", "image/jpeg", "image/gif", "image/webp", "image/avif"];

function outputView(output: NbOutput): NotebookOutput | null {
  if (output.output_type === "stream") {
    return textOutput(sourceText(output.text), output.name === "stderr");
  }
  if (output.output_type === "error") {
    return {
      kind: "error",
      ename: output.ename ?? "Error",
      evalue: output.evalue ?? "",
      traceHtml: ansiToHtml(capText((output.traceback ?? []).join("\n")).text),
    };
  }
  if (output.output_type === "display_data" || output.output_type === "execute_result") {
    const data = output.data ?? {};
    for (const mime of RASTER_MIMES) {
      if (mime in data) {
        const src = imageDataUrl(mime, data[mime]);
        if (src !== null) return { kind: "image", src, alt: mime };
      }
    }
    if ("image/svg+xml" in data) {
      const src = svgDataUrl(data["image/svg+xml"]);
      if (src !== null) return { kind: "image", src, alt: "svg" };
    }
    if ("text/html" in data) {
      return { kind: "html", html: sourceText(data["text/html"]) };
    }
    if ("application/json" in data) {
      let pretty: string;
      try {
        pretty = JSON.stringify(data["application/json"], null, 2);
      } catch {
        pretty = String(data["application/json"]);
      }
      return textOutput(pretty, false);
    }
    if ("text/plain" in data) {
      return textOutput(sourceText(data["text/plain"]), false);
    }
  }
  return null;
}

function cellView(cell: NbCell): NotebookCell {
  const source = sourceText(cell.source);
  const type =
    cell.cell_type === "markdown" ? "markdown" : cell.cell_type === "code" ? "code" : "raw";
  return {
    type,
    source,
    execCount: type === "code" && typeof cell.execution_count === "number" ? cell.execution_count : null,
    outputs:
      type === "code"
        ? (cell.outputs ?? [])
            .map(outputView)
            .filter((view): view is NotebookOutput => view !== null)
        : [],
  };
}

/** Parse raw ipynb JSON text; throws Error with a user-facing message on bad input. */
export function parseNotebook(text: string): NotebookModel {
  let doc: NbDocument;
  try {
    doc = JSON.parse(text) as NbDocument;
  } catch {
    throw new Error("notebook JSON 解析失败（可切换到源码模式查看原文）");
  }
  if (!Array.isArray(doc.cells)) {
    throw new Error("不是有效的 notebook：缺少 cells（可切换到源码模式查看原文）");
  }
  return {
    language: doc.metadata?.language_info?.name ?? "python",
    cells: doc.cells.map(cellView),
  };
}
