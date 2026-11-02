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
