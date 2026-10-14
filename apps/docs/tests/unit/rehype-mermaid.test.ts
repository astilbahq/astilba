import { describe, expect, it } from "vitest";

import { rehypeMermaid } from "../../src/docs/rehype-mermaid";

type HastTree = Parameters<ReturnType<typeof rehypeMermaid>>[0];

const createCodeBlock = (language: string, lines: string[]): HastTree => ({
  children: [
    {
      children: lines.map((value) => ({ type: "text", value })),
      properties: { className: [`language-${language}`] },
      tagName: "code",
      type: "element",
    },
  ],
  properties: {},
  tagName: "pre",
  type: "element",
});

const createRoot = (...children: HastTree[]): HastTree => ({
  children,
  type: "root",
});

describe("Mermaid code fences", () => {
  it("replaces a Mermaid fence with a diagram host that keeps its source", () => {
    const tree = createRoot(
      createCodeBlock("mermaid", ["flowchart LR\n", "  A --> B"])
    );

    rehypeMermaid()(tree);

    expect(tree.children).toEqual([
      {
        children: [{ type: "text", value: "flowchart LR\n  A --> B" }],
        properties: { className: ["mermaid-diagram"], dataMermaid: "" },
        tagName: "div",
        type: "element",
      },
    ]);
  });

  it("finds Mermaid fences nested inside other elements", () => {
    const tree = createRoot({
      children: [createCodeBlock("mermaid", ["sequenceDiagram"])],
      properties: {},
      tagName: "section",
      type: "element",
    });

    rehypeMermaid()(tree);

    expect(tree.children?.[0]?.children?.[0]?.tagName).toBe("div");
  });

  it("leaves other code fences for Expressive Code", () => {
    const codeBlock = createCodeBlock("ts", ["const a = 1;"]);
    const tree = createRoot(codeBlock);

    rehypeMermaid()(tree);

    expect(tree.children?.[0]).toBe(codeBlock);
  });
});
