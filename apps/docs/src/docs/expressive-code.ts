import type { StarlightExpressiveCodeOptions } from "@astrojs/starlight/expressive-code";

type ExpressiveCodePlugin = Extract<
  NonNullable<StarlightExpressiveCodeOptions["plugins"]>[number],
  { name: string }
>;
type PostprocessRenderedBlockHook = NonNullable<
  NonNullable<ExpressiveCodePlugin["hooks"]>["postprocessRenderedBlock"]
>;
type RenderedBlockAst =
  Parameters<PostprocessRenderedBlockHook>[0]["renderData"]["blockAst"];

export const removeUntitledTerminalWindowHeader = (
  blockAst: RenderedBlockAst
): void => {
  const frameClassNames = blockAst.properties.className;

  if (
    !Array.isArray(frameClassNames) ||
    !frameClassNames.includes("is-terminal") ||
    frameClassNames.includes("has-title")
  ) {
    return;
  }

  blockAst.children = blockAst.children.filter((child) => {
    if (child.type !== "element" || child.tagName !== "figcaption") {
      return true;
    }

    const classNames = child.properties.className;

    return !(Array.isArray(classNames) && classNames.includes("header"));
  });
};

export const removeTerminalWindowHeaders = {
  hooks: {
    postprocessRenderedBlock: ({ renderData }) => {
      removeUntitledTerminalWindowHeader(renderData.blockAst);
    },
  },
  name: "Remove terminal window headers",
} satisfies ExpressiveCodePlugin;
