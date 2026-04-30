import MarkdownIt from "markdown-it";
import anchor from "markdown-it-anchor";
import attrs from "markdown-it-attrs";
import deflist from "markdown-it-deflist";
import footnote from "markdown-it-footnote";
import mark from "markdown-it-mark";
import sub from "markdown-it-sub";
import sup from "markdown-it-sup";
import taskLists from "markdown-it-task-lists";
import texmath from "markdown-it-texmath";
import hljs from "highlight.js";
import katex from "katex";
import mermaid from "mermaid";
import { nextTick } from "vue";

mermaid.initialize({ startOnLoad: false, securityLevel: "loose" });

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function escapeAttribute(value: string): string {
  return escapeHtml(value).replace(/'/g, "&#39;");
}

function normalizeLanguage(info: string): string {
  return info.trim().split(/\s+/)[0].toLowerCase();
}

function codeLanguageClass(language: string): string {
  return language ? ` class="language-${escapeAttribute(language)}"` : "";
}

function highlightedLine(line: string, language: string): string {
  if (!line) return " ";
  if (language && hljs.getLanguage(language)) {
    return hljs.highlight(line, { language, ignoreIllegals: true }).value || " ";
  }
  return escapeHtml(line);
}

function renderCodeFence(code: string, language: string): string {
  const lines = code.replace(/\n$/, "").split("\n");
  const rows = (lines.length ? lines : [""]).map((line, index) => {
    const number = String(index + 1);
    return [
      '<span class="markdown-code-line">',
      `<span class="markdown-code-line-number" aria-hidden="true">${number}</span>`,
      `<span class="markdown-code-line-content">${highlightedLine(line, language)}</span>`,
      "</span>",
    ].join("");
  });
  return `<pre class="hljs markdown-code-block"><code${codeLanguageClass(language)}>${rows.join("")}</code></pre>`;
}

function looksLikeMathOnly(content: string): boolean {
  const trimmed = content.trim();
  if (!trimmed) return false;
  if (trimmed.length > 3000) return false;
  if (/[A-Za-z]{4,}(?:\s+[A-Za-z]{2,}){2,}/.test(trimmed.replace(/\\(?:mathrm|mathit|mathbf|text)\{[^}]*\}/g, ""))) return false;
  return /\\[A-Za-z]+|[_^{}]|\bin\b|[=<>+\-*/[\](),]/.test(trimmed);
}

function stripMathWrapper(content: string): string {
  const trimmed = content.trim();
  const bracketMatch = trimmed.match(/^\\\[((?:.|\n)*)\\\]$/);
  if (bracketMatch) return bracketMatch[1].trim();
  const parenMatch = trimmed.match(/^\\\(((?:.|\n)*)\\\)$/);
  if (parenMatch) return parenMatch[1].trim();
  const dollarMatch = trimmed.match(/^\$\$((?:.|\n)*)\$\$$/);
  if (dollarMatch) return dollarMatch[1].trim();
  return trimmed;
}

function renderMathOnlyFence(code: string): string | null {
  if (!looksLikeMathOnly(code)) return null;
  try {
    return `<section class="markdown-fence-block markdown-math-fence">${katex.renderToString(stripMathWrapper(code), {
      displayMode: true,
      throwOnError: false,
    })}</section>`;
  } catch {
    return null;
  }
}

function renderMarkdownFence(code: string): string {
  return renderMathOnlyFence(code) ?? `<section class="markdown-fence-block">${md.render(code)}</section>`;
}

const md: MarkdownIt = new MarkdownIt({
  html: true,
  linkify: true,
  typographer: true,
  highlight(code: string, language: string): string {
    return renderCodeFence(code, language.toLowerCase());
  },
})
  .use(sub)
  .use(sup)
  .use(mark)
  .use(footnote)
  .use(deflist)
  .use(taskLists, { enabled: false })
  .use(anchor)
  .use(attrs)
  .use(texmath, {
    engine: katex,
    delimiters: ["dollars", "brackets", "beg_end"],
    katexOptions: { throwOnError: false },
  });

const fence = md.renderer.rules.fence;
md.renderer.rules.fence = (tokens: any[], idx: number, options: any, env: any, self: any): string => {
  const token = tokens[idx];
  const language = normalizeLanguage(token.info);
  if (language === "mermaid") {
    return `<pre class="mermaid">${md.utils.escapeHtml(token.content)}</pre>`;
  }
  if (language === "latex" || language === "tex") {
    return renderMarkdownFence(token.content);
  }
  return fence ? fence(tokens, idx, options, env, self) : self.renderToken(tokens, idx, options);
};

export function renderMarkdown(source: string): string {
  return md.render(source);
}

export async function renderMermaidIn(container: HTMLElement | null, idPrefix = "mermaid"): Promise<void> {
  await nextTick();
  if (!container) return;
  const nodes = Array.from(container.querySelectorAll<HTMLElement>(".mermaid"));
  await Promise.all(
    nodes.map(async (node, index) => {
      const source = node.textContent ?? "";
      try {
        const result = await mermaid.render(`${idPrefix}-${Date.now()}-${index}`, source);
        node.outerHTML = result.svg;
      } catch {
        node.classList.add("mermaid-error");
      }
    }),
  );
}
