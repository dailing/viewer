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

const md: MarkdownIt = new MarkdownIt({
  html: true,
  linkify: true,
  typographer: true,
  highlight(code: string, language: string): string {
    if (language && hljs.getLanguage(language)) {
      return `<pre class="hljs"><code>${hljs.highlight(code, { language }).value}</code></pre>`;
    }
    return `<pre class="hljs"><code>${escapeHtml(code)}</code></pre>`;
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
    delimiters: "dollars",
    katexOptions: { throwOnError: false },
  });

const fence = md.renderer.rules.fence;
md.renderer.rules.fence = (tokens: any[], idx: number, options: any, env: any, self: any): string => {
  const token = tokens[idx];
  if (token.info.trim().split(/\s+/)[0] === "mermaid") {
    return `<pre class="mermaid">${md.utils.escapeHtml(token.content)}</pre>`;
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
