interface HastNode {
  children?: HastNode[];
  properties?: Record<string, unknown>;
  tagName?: string;
  type: string;
  value?: string;
}

const collectText = (node: HastNode): string =>
  node.type === "text"
    ? (node.value ?? "")
    : (node.children ?? []).map(collectText).join("");

const hasMermaidLanguage = (node: HastNode): boolean => {
  const classes = node.properties?.className;
  return Array.isArray(classes) && classes.includes("language-mermaid");
};

export const rehypeMermaid =
  () =>
  (tree: HastNode): void => {
    const visit = (node: HastNode): void => {
      const children = node.children ?? [];

      for (const [index, child] of children.entries()) {
        const code = child.children?.find(
          (candidate) => candidate.tagName === "code"
        );

        if (child.tagName === "pre" && code && hasMermaidLanguage(code)) {
          children[index] = {
            type: "element",
            tagName: "div",
            properties: {
              className: ["mermaid-diagram"],
              dataMermaid: "",
            },
            children: [{ type: "text", value: collectText(code) }],
          };
        } else {
          visit(child);
        }
      }
    };

    visit(tree);
  };
