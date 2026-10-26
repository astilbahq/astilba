import { css } from "../../../styled-system/css";

export const page = css({
  inlineSize: "full",
  maxInlineSize: "2xl",
  marginInline: "auto",
  color: "fg",
  fontFamily: "ui",
  fontSize: "sm",
  lineHeight: "body",
  paddingBlock: { base: "2", md: "6" },
  "& h1": {
    fontSize: "2xl",
    lineHeight: "heading",
    fontWeight: "semibold",
    letterSpacing: "heading",
  },
  "& h2": { fontSize: "lg", lineHeight: "heading", fontWeight: "semibold" },
});
export const header = css({
  display: "flex",
  flexDirection: "column",
  gap: "2",
  paddingBlockEnd: "8",
});
export const section = css({
  paddingBlock: "7",
  borderBlockStartWidth: "1px",
  borderBlockStartStyle: "solid",
  borderBlockStartColor: "separator",
  display: "flex",
  flexDirection: "column",
  gap: "5",
});
export const intro = css({
  display: "flex",
  flexDirection: "column",
  gap: "2",
});
export const task = css({
  inlineSize: "full",
  maxInlineSize: "auth.content",
  display: "flex",
  flexDirection: "column",
  gap: "4",
});
export const actions = css({
  display: "flex",
  flexWrap: "wrap",
  alignItems: "center",
  gap: "3",
});
