import { describe, expect, it } from "vitest";

import { getDocsSourcePath, getDocsSourceUrl } from "../../src/docs/source";

describe("documentation source links", () => {
  it("extracts repository-relative source paths", () => {
    expect(
      getDocsSourcePath(
        "/workspace/src/content/docs/cache/overview.md",
        "cache/overview"
      )
    ).toBe("src/content/docs/cache/overview.md");
    expect(
      getDocsSourcePath(
        "C:\\workspace\\src\\content\\docs\\cache\\overview.md",
        "cache/overview"
      )
    ).toBe("src/content/docs/cache/overview.md");
  });

  it("falls back to the collection id and creates a public URL", () => {
    expect(getDocsSourcePath(undefined, "cache/overview")).toBe(
      "src/content/docs/cache/overview.md"
    );
    expect(getDocsSourceUrl(undefined, "cache/overview")).toBe(
      "https://github.com/astilbahq/docs/blob/main/src/content/docs/cache/overview.md"
    );
  });

  it("preserves an MDX extension when a source path is outside the content root", () => {
    expect(
      getDocsSourcePath("/generated/cache/example.mdx", "cache/example")
    ).toBe("src/content/docs/cache/example.mdx");
  });
});
