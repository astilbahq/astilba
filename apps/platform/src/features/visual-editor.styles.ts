import { css } from "../../styled-system/css";

export const header = css({
  display: "flex",
  flexWrap: "wrap",
  alignItems: "center",
  justifyContent: "space-between",
  gap: "3",
});
export const heading = css({ fontSize: "md", fontWeight: "semibold" });
export const views = css({ display: "flex", flexWrap: "wrap", gap: "1" });
export const view = css({
  "&[aria-pressed=true]": { backgroundColor: "navigation.active" },
});
export const layout = css({
  display: "flex",
  flexWrap: "wrap",
  alignItems: "center",
  gap: "3",
  justifyContent: "space-between",
});

export const workspace = css({
  containerType: "inline-size",
  containerName: "visual-editor",
  "& > details": {
    display: "block",
    borderBlockStartWidth: "1px",
    borderBlockStartStyle: "solid",
    borderBlockStartColor: "separator",
    "& > summary": {
      paddingBlock: "3",
      minBlockSize: "control.sm",
      cursor: "pointer",
      color: "fg",
      fontSize: "secondary",
      borderRadius: "control",
      _hover: { color: "subtle" },
      _focusVisible: {
        outlineWidth: "2px",
        outlineStyle: "solid",
        outlineColor: "link.focus",
        outlineOffset: "0.5",
      },
    },
  },
});

export const editor = css({
  display: "grid",
  gap: "5",
  minInlineSize: "0",
  alignItems: "start",
  "@container visual-editor (min-width: 56rem)": {
    gridTemplateColumns: "minmax(0, 1fr) token(sizes.visual.inspector)",
  },
});
export const graph = css({
  display: "grid",
  gap: "3",
  minInlineSize: "0",
  _focusVisible: {
    outlineWidth: "2px",
    outlineStyle: "solid",
    outlineColor: "link.focus",
    outlineOffset: "0.5",
    borderRadius: "control",
  },
});
export const inspector = css({
  display: "grid",
  gap: "5",
  minInlineSize: "0",
  paddingBlock: "4",
  borderBlockStartWidth: "1px",
  borderBlockStartStyle: "solid",
  borderBlockStartColor: "separator",
  "@container visual-editor (min-width: 56rem)": {
    maxBlockSize: "visual.canvas",
    overflow: "auto",
    overflowWrap: "anywhere",
    overscrollBehavior: "contain",
    paddingBlock: "1",
    paddingInline: "4",
    borderBlockStartWidth: "0",
    borderInlineStartWidth: "1px",
    borderInlineStartStyle: "solid",
    borderInlineStartColor: "separator",
    scrollbarGutter: "stable",
  },
});
export const inspectorHeader = css({
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: "3",
  minInlineSize: "0",
});
export const inspectorTitle = css({
  fontSize: "md",
  fontWeight: "semibold",
  overflowWrap: "anywhere",
});
export const resource = css({
  display: "flex",
  alignItems: "flex-start",
  gap: "3",
  textAlign: "start",
  minInlineSize: "0",
});
export const resourceIcon = css({
  display: "flex",
  color: "subtle",
  paddingBlockStart: "0.5",
  "--icon-size": "token(sizes.4)",
});
export const resourceText = css({
  display: "grid",
  gap: "1",
  minInlineSize: "0",
});
export const resourceName = css({
  fontSize: "sm",
  fontWeight: "semibold",
  overflowWrap: "anywhere",
});
export const resourceKind = css({ color: "subtle", fontSize: "secondary" });
export const resourceDetail = css({
  color: "subtle",
  fontSize: "secondary",
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
});
export const resourceList = css({
  display: "grid",
  gap: "2",
  listStyle: "none",
  padding: "0",
  margin: "0",
});
export const resourceChoice = css({
  inlineSize: "full",
  justifyContent: "flex-start",
  paddingInline: "2",
  paddingBlock: "1",
  minBlockSize: "control.sm",
  '&[aria-pressed="true"]': { backgroundColor: "navigation.active" },
});

export const emptyCanvas = css({
  position: "absolute",
  insetBlock: "0",
  insetInline: "0",
  display: "flex",
  flexDirection: "column",
  alignItems: "center",
  justifyContent: "center",
  padding: "8",
  gap: "2",
  textAlign: "center",
  pointerEvents: "none",
  zIndex: "visual",
});
