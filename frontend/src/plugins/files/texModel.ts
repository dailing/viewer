/** Pure LaTeX source normalization and companion-path handling. */

/** Extract a brace-balanced group starting at `start` (index of '{'). */
export function readGroup(src: string, start: number): string | null {
  let depth = 0;
  for (let index = start; index < src.length; index++) {
    const char = src[index];
    if (char === "\\") {
      index++;
      continue;
    }
    if (char === "{") depth++;
    else if (char === "}") {
      depth--;
      if (depth === 0) return src.slice(start + 1, index);
    }
  }
  return null;
}

/** Normalize publisher-class frontmatter unsupported by faster-latex without
 * changing the meaning of the paper body. */
export function normalizeFrontmatter(source: string): string {
  let out = source;
  out = out.replace(/\\(author|title)\*?\s*\[[^\]]*\]/g, "\\$1");
  out = out.replace(/\\affil\s*(?:\[[^\]]*\])?\s*\{/g, "\\thanks{");
  const abstract = /\\abstract\s*\{/.exec(out);
  if (abstract !== null) {
    const brace = out.indexOf("{", abstract.index);
    const body = readGroup(out, brace);
    if (body !== null) {
      out = `${out.slice(0, abstract.index)}\\begin{abstract}\n${body}\n\\end{abstract}${out.slice(brace + body.length + 2)}`;
    }
  }
  out = out.replace(/\\FloatBarrier\b/g, "");
  const unwrap = /\\(?:fnm|sur|orgdiv|orgname|orgaddress|city|postcode|country|equalcontrib|correspondingauthor)\{([^{}]*)\}/g;
  let previous: string;
  do {
    previous = out;
    out = out.replace(unwrap, "$1");
  } while (out !== previous);
  return out;
}

/** Strip unescaped-% line comments so commented-out inputs are not fetched. */
export function stripComments(source: string): string {
  return source.replace(/(?<!\\)%.*/g, "");
}

/** Companion names exactly as the engine looks them up. */
export function depNames(source: string): string[] {
  const clean = stripComments(source);
  const out: string[] = [];
  const pattern = /\\(?:input|include)\s*\{([^}]+)\}|\\(?:bibliography|addbibresource)\s*\{([^}]+)\}/g;
  for (const match of clean.matchAll(pattern)) {
    const input = match[1];
    if (input !== undefined) {
      const name = input.trim();
      if (name !== "") out.push(/\.[^./]+$/.test(name) ? name : `${name}.tex`);
      continue;
    }
    const bibliography = match[2];
    if (bibliography !== undefined) {
      for (const part of bibliography.split(",")) {
        const name = part.trim();
        if (name !== "") out.push(/\.[^./]+$/.test(name) ? name : `${name}.bib`);
      }
    }
  }
  return out;
}

/** Join a base directory with a project-relative name, resolving . and .. */
export function normalizeJoin(dir: string, rel: string): string {
  const combined = rel.startsWith("/") || dir === "" ? rel : `${dir}/${rel}`;
  const absolute = combined.startsWith("/");
  const parts: string[] = [];
  for (const segment of combined.split("/")) {
    if (segment === "" || segment === ".") continue;
    if (segment === "..") {
      parts.pop();
      continue;
    }
    parts.push(segment);
  }
  return (absolute ? "/" : "") + parts.join("/");
}
