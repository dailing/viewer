/**
 * ANSI escape → HTML renderer for notebook text outputs (framework v0.94).
 *
 * SGR colours and styles become <span> elements: 16-colour palette,
 * 256-colour cube/grayscale and truecolor, bold/dim/italic/underline/
 * strikethrough/inverse. Carriage returns get real terminal line-overwrite
 * semantics (progress bars collapse to their final visible state), backspace
 * moves the cursor left, and CSI K erases parts of the current line. All
 * other escape sequences (cursor movement, OSC hyperlinks, …) are dropped.
 * Text is HTML-escaped; the output is safe for v-html inside a <pre>.
 *
 * Pure module: no Vue, no DOM — unit-testable in plain Node.
 */

/** xterm palette. Black-on-dark-theme invisibility is accepted (same trade-off Jupyter makes). */
const STANDARD = [
  "#000000",
  "#cd0000",
  "#00cd00",
  "#cdcd00",
  "#0000ee",
  "#cd00cd",
  "#00cdcd",
  "#e5e5e5",
];
const BRIGHT = [
  "#7f7f7f",
  "#ff0000",
  "#00ff00",
  "#ffff00",
  "#5c5cff",
  "#ff00ff",
  "#00ffff",
  "#ffffff",
];

function color256(n: number): string {
  if (n < 8) return STANDARD[n];
  if (n < 16) return BRIGHT[n - 8];
  if (n < 232) {
    const i = n - 16;
    const level = (v: number): number => (v === 0 ? 0 : 55 + v * 40);
    return `rgb(${level(Math.floor(i / 36))},${level(Math.floor((i % 36) / 6))},${level(i % 6)})`;
  }
  const v = 8 + (n - 232) * 10;
  return `rgb(${v},${v},${v})`;
}

interface Style {
  fg: string | null;
  bg: string | null;
  bold: boolean;
  dim: boolean;
  italic: boolean;
  underline: boolean;
  strike: boolean;
  inverse: boolean;
}

function plainStyle(): Style {
  return {
    fg: null,
    bg: null,
    bold: false,
    dim: false,
    italic: false,
    underline: false,
    strike: false,
    inverse: false,
  };
}

/** Rendered CSS for the current style; "" = plain text (emitted unwrapped). */
function styleCss(style: Style): string {
  let fg = style.fg;
  let bg = style.bg;
  if (style.inverse) [fg, bg] = [bg, fg];
  const parts: string[] = [];
  if (fg !== null) parts.push(`color:${fg}`);
  if (bg !== null) parts.push(`background-color:${bg}`);
  if (style.bold) parts.push("font-weight:600");
  if (style.dim) parts.push("opacity:0.7");
  if (style.italic) parts.push("font-style:italic");
  const decorations = [style.underline ? "underline" : "", style.strike ? "line-through" : ""].filter(
    (value) => value !== "",
  );
  if (decorations.length > 0) parts.push(`text-decoration:${decorations.join(" ")}`);
  return parts.join(";");
}

/** Read one SGR extended colour (38/48): 5;<n> or 2;<r>;<g>;<b>. Returns [color, consumed]. */
function extendedColor(params: number[], index: number): [string | null, number] {
  const mode = params[index + 1];
  if (mode === 5 && index + 2 < params.length) return [color256(params[index + 2] & 0xff), 3];
  if (mode === 2 && index + 4 < params.length) {
    return [`rgb(${params[index + 2] & 0xff},${params[index + 3] & 0xff},${params[index + 4] & 0xff})`, 5];
  }
  return [null, 2];
}

/** Apply SGR params; returns the new current CSS ("" for plain). */
function applySgr(style: Style, params: number[]): string {
  const list = params.length === 0 ? [0] : params;
  for (let i = 0; i < list.length; i++) {
    const code = list[i];
    if (code === 0) Object.assign(style, plainStyle());
    else if (code === 1) style.bold = true;
    else if (code === 2) style.dim = true;
    else if (code === 3) style.italic = true;
    else if (code === 4) style.underline = true;
    else if (code === 7) style.inverse = true;
    else if (code === 9) style.strike = true;
    else if (code === 22) {
      style.bold = false;
      style.dim = false;
    } else if (code === 23) style.italic = false;
    else if (code === 24) style.underline = false;
    else if (code === 27) style.inverse = false;
    else if (code === 29) style.strike = false;
    else if (code >= 30 && code <= 37) style.fg = STANDARD[code - 30];
    else if (code === 38) {
      const [color, consumed] = extendedColor(list, i);
      if (color !== null) style.fg = color;
      i += consumed - 1;
    } else if (code === 39) style.fg = null;
    else if (code >= 40 && code <= 47) style.bg = STANDARD[code - 40];
    else if (code === 48) {
      const [color, consumed] = extendedColor(list, i);
      if (color !== null) style.bg = color;
      i += consumed - 1;
    } else if (code === 49) style.bg = null;
    else if (code >= 90 && code <= 97) style.fg = BRIGHT[code - 90];
    else if (code >= 100 && code <= 107) style.bg = BRIGHT[code - 100];
  }
  return styleCss(style);
}

interface Cell {
  char: string;
  css: string;
}

const ESCAPE_RE = /\x1b\[([0-9;?]*)([ -/]*)([@-~])|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g;

function escapeHtml(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/** Emit one line: consecutive cells sharing one CSS collapse into one span. */
function flushLine(line: Cell[], out: string[]): void {
  let index = 0;
  while (index < line.length) {
    const css = line[index].css;
    let end = index + 1;
    while (end < line.length && line[end].css === css) end++;
    const text = escapeHtml(line.slice(index, end).map((cell) => cell.char).join(""));
    out.push(css === "" ? text : `<span style="${css}">${text}</span>`);
    index = end;
  }
  line.length = 0;
}

/**
 * Render text with ANSI escapes to HTML. Unterminated sequences at the end of
 * the (possibly truncated) input are tolerated.
 */
export function ansiToHtml(text: string): string {
  const style = plainStyle();
  const out: string[] = [];
  const line: Cell[] = [];
  let css = "";
  let cursor = 0;

  /** Consume visible text between escapes, honouring \n \r \b control chars. */
  function feed(chunk: string): void {
    for (const char of chunk) {
      if (char === "\n") {
        flushLine(line, out);
        out.push("\n");
        cursor = 0;
      } else if (char === "\r") {
        cursor = 0;
      } else if (char === "\b") {
        cursor = Math.max(0, cursor - 1);
      } else {
        if (cursor < line.length) line[cursor] = { char, css };
        else line.push({ char, css });
        cursor++;
      }
    }
  }

  let index = 0;
  for (const match of text.matchAll(ESCAPE_RE)) {
    feed(text.slice(index, match.index));
    const final = match[3];
    if (final === "m") {
      const params = (match[1] ?? "")
        .split(";")
        .filter((part) => part !== "")
        .map((part) => Number.parseInt(part, 10) || 0);
      css = applySgr(style, params);
    } else if (final === "K") {
      const param = Number.parseInt(match[1] ?? "0", 10) || 0;
      if (param === 2) {
        line.length = 0;
        cursor = 0;
      } else if (param === 1) {
        const blank: Cell = { char: " ", css };
        for (let i = 0; i < cursor; i++) line[i] = blank;
      } else {
        line.length = Math.min(line.length, cursor);
      }
    }
    // Other CSI finals (cursor movement, …) and OSC are dropped.
    index = match.index + match[0].length;
  }
  feed(text.slice(index));
  flushLine(line, out);
  return out.join("");
}
