import { describe, expect, it } from "vitest";
import { depNames, normalizeFrontmatter, normalizeJoin, readGroup, stripComments } from "./texModel";

describe("texModel", () => {
  it("reads balanced groups without being confused by escaped braces", () => {
    expect(readGroup("{a{b}c} trailing", 0)).toBe("a{b}c");
    expect(readGroup("{a\\}b}c", 0)).toBe("a\\}b");
    expect(readGroup("{unterminated", 0)).toBeNull();
  });

  it("normalizes publisher frontmatter", () => {
    const source = "\\title[Short]{Long title}\n\\author[1]{\\fnm{A} \\sur{B}}\n\\affil[1]{Lab}\n\\abstract{Body {nested}}\n\\FloatBarrier";
    const result = normalizeFrontmatter(source);
    expect(result).toContain("\\title{Long title}");
    expect(result).toContain("\\author{A B}");
    expect(result).toContain("\\thanks{Lab}");
    expect(result).toContain("\\begin{abstract}\nBody {nested}\n\\end{abstract}");
    expect(result).not.toContain("\\FloatBarrier");
  });

  it("finds dependencies while ignoring comments", () => {
    const source = "% \\input{ignored}\n\\input{chapter}\n\\include{extras/setup.tex}\n\\bibliography{one,two.bib}";
    expect(depNames(source)).toEqual(["chapter.tex", "extras/setup.tex", "one.bib", "two.bib"]);
    expect(stripComments("text % comment\n\\% kept")).toBe("text \n\\% kept");
  });

  it("normalizes relative and absolute companion paths", () => {
    expect(normalizeJoin("/paper/main", "../figures/a.pdf")).toBe("/paper/figures/a.pdf");
    expect(normalizeJoin("/paper", "/absolute/a.tex")).toBe("/absolute/a.tex");
    expect(normalizeJoin("", "a/./b.tex")).toBe("a/b.tex");
  });
});
