import { css } from "../styled-system/css";

const buttonBase = css.raw({
  display: "inline-flex",
  alignItems: "center",
  justifyContent: "center",
  gap: "2",
  minHeight: "40px",
  px: "3",
  border: "1px solid",
  borderColor: "control",
  rounded: "md",
  bg: "paper",
  color: "ink",
  fontWeight: "500",
  whiteSpace: "nowrap",
  _hover: { bg: "canvas" },
  _expanded: { bg: "tint", borderColor: "accent" },
  "&[aria-pressed=true]": {
    bg: "tint",
    color: "accent",
    borderColor: "accent",
  },
});
export const button = css(buttonBase);
export const listButton = css(buttonBase, {
  width: "100%",
  display: "block",
  textAlign: "left",
  p: "4",
  whiteSpace: "normal",
  overflowWrap: "anywhere",
});
export const primary = css(buttonBase, {
  bg: "accent",
  color: "white",
  borderColor: "accent",
  _hover: { bg: "#5733ad" },
});
export const input = css({
  width: "100%",
  minWidth: "0",
  minHeight: "40px",
  px: "3",
  py: "2",
  bg: "paper",
  border: "1px solid",
  borderColor: "control",
  rounded: "md",
  _invalid: { borderColor: "danger" },
});
export const stack = css({
  display: "flex",
  flexDirection: "column",
  gap: "4",
});
export const row = css({
  display: "flex",
  alignItems: "center",
  gap: "2",
  flexWrap: "wrap",
});
export const label = css({
  display: "flex",
  flexDirection: "column",
  gap: "2",
  fontWeight: "500",
});
export const muted = css({
  color: "muted",
  fontSize: "12px",
  lineHeight: "1.6",
});
export const error = css({
  color: "danger",
  fontSize: "13px",
  lineHeight: "1.5",
});
