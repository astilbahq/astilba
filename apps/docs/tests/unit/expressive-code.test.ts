import { describe, expect, it } from "vitest";

import { removeUntitledTerminalWindowHeader } from "../../src/docs/expressive-code";

type RenderedBlockAst = Parameters<
  typeof removeUntitledTerminalWindowHeader
>[0];

const createTerminalFrame = (hasTitle: boolean): RenderedBlockAst => ({
  children: [
    {
      children: [],
      properties: { className: ["header"] },
      tagName: "figcaption",
      type: "element",
    },
    {
      children: [],
      properties: {},
      tagName: "pre",
      type: "element",
    },
  ],
  properties: {
    className: ["frame", "is-terminal", ...(hasTitle ? ["has-title"] : [])],
  },
  tagName: "figure",
  type: "element",
});

describe("Expressive Code terminal frames", () => {
  it("removes the generated caption from an untitled terminal", () => {
    const frame = createTerminalFrame(false);

    removeUntitledTerminalWindowHeader(frame);

    expect(
      frame.children.some(
        (child) => child.type === "element" && child.tagName === "figcaption"
      )
    ).toBe(false);
  });

  it("preserves captions on non-terminal frames", () => {
    const frame = createTerminalFrame(false);
    frame.properties.className = ["frame"];

    removeUntitledTerminalWindowHeader(frame);

    expect(
      frame.children.some(
        (child) => child.type === "element" && child.tagName === "figcaption"
      )
    ).toBe(true);
  });

  it("preserves an authored terminal title", () => {
    const frame = createTerminalFrame(true);

    removeUntitledTerminalWindowHeader(frame);

    expect(
      frame.children.some(
        (child) => child.type === "element" && child.tagName === "figcaption"
      )
    ).toBe(true);
  });
});
