import { css } from "../../../styled-system/css";

export const page = css({
  display: "grid",
  gap: "8",
  color: "fg",
  fontFamily: "ui",
  fontSize: "sm",
  lineHeight: "body",
});
export const header = css({
  display: "flex",
  justifyContent: "space-between",
  alignItems: "center",
  flexWrap: "wrap",
  gap: "4",
});
export const intro = css({ display: "grid", gap: "2", maxInlineSize: "prose" });
export const title = css({
  fontSize: "2xl",
  fontWeight: "semibold",
  lineHeight: "heading",
  letterSpacing: "heading",
});
export const heading = css({
  fontSize: "lg",
  fontWeight: "semibold",
  lineHeight: "heading",
});
export const muted = css({ color: "subtle", overflowWrap: "anywhere" });
export const state = css({
  display: "grid",
  gap: "3",
  justifyItems: "start",
  paddingBlock: "8",
  maxInlineSize: "prose",
});
export const form = css({
  display: "grid",
  gap: "4",
  inlineSize: "full",
  maxInlineSize: "auth.content",
});
export const actions = css({
  display: "flex",
  flexWrap: "wrap",
  alignItems: "center",
  gap: "3",
});
export const list = css({
  display: "grid",
  borderBlockStartWidth: "1px",
  borderBlockStartStyle: "solid",
  borderBlockStartColor: "separator",
});
export const row = css({
  display: "flex",
  flexWrap: "wrap",
  justifyContent: "space-between",
  alignItems: "center",
  gap: "3",
  paddingBlock: "5",
  paddingInline: "3",
  borderBlockEndWidth: "1px",
  borderBlockEndStyle: "solid",
  borderBlockEndColor: "separator",
  color: "fg",
  textDecoration: "none",
  borderRadius: "control",
  transitionProperty: "background-color",
  transitionDuration: "button",
  transitionTimingFunction: "out",
  _hover: { backgroundColor: "action.ghostHover", textDecoration: "none" },
  _motionReduce: { transitionProperty: "none" },
});
export const name = css({
  fontWeight: "medium",
  overflowWrap: "anywhere",
  minInlineSize: "0",
});
export const open = css({
  color: "subtle",
  display: "inline-flex",
  alignItems: "center",
  gap: "2",
  fontSize: "secondary",
});
