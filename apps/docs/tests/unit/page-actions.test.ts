import { describe, expect, it } from "vitest";

import { createPageActionDestinations } from "../../src/docs/page-actions";

describe("page action destinations", () => {
  const markdownUrl = "https://astilba.com/docs/cache/overview.md";
  const sourceUrl =
    "https://github.com/astilbahq/docs/blob/main/src/content/docs/cache/overview.md";
  const destinations = createPageActionDestinations(markdownUrl, sourceUrl);

  it("keeps the public source as the GitHub destination", () => {
    expect(destinations[0]).toEqual({
      href: sourceUrl,
      id: "github",
      label: "GitHub",
    });
  });

  it("builds one encoded prompt for every assistant", () => {
    expect(destinations.map(({ id }) => id)).toEqual([
      "github",
      "chatgpt",
      "claude",
      "t3-chat",
      "copilot",
      "cursor",
    ]);

    const expectedPrompt = `Read ${markdownUrl}, I want to ask questions about it.`;

    for (const destination of destinations.slice(1)) {
      const url = new URL(destination.href);
      const parameter = destination.id === "cursor" ? "text" : "q";

      expect(url.searchParams.get(parameter)).toBe(expectedPrompt);
    }

    const chatgpt = new URL(
      destinations.find(({ id }) => id === "chatgpt")?.href ?? ""
    );
    expect(chatgpt.searchParams.get("hints")).toBe("search");
  });
});
