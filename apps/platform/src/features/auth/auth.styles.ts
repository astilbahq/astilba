import { css } from "../../../styled-system/css";

export const palette = css({
  "--colors-fg": "token(colors.auth.fg)",
  "--colors-subtle": "token(colors.auth.subtle)",
  "--colors-surface": "token(colors.auth.surface)",
  "--colors-page": "token(colors.auth.page)",
  "--colors-border": "token(colors.auth.border)",
  "--colors-brand": "token(colors.auth.action)",
  "--colors-link-fg": "token(colors.auth.action)",
  "--colors-link-focus": "token(colors.auth.focus)",
  "--colors-soft": "token(colors.auth.soft)",
  "--colors-danger": "token(colors.auth.danger)",
  "--colors-error": "token(colors.auth.danger)",
  "--colors-control-border": "token(colors.auth.border)",
  "--colors-action-solid": "token(colors.auth.action)",
  "--colors-action-contrast": "token(colors.auth.onAction)",
  "--colors-action-hover": "token(colors.auth.actionHover)",
  fontFamily: "ui",
});

export const appearance = css({
  display: "flex",
  gap: "2",
  position: "absolute",
  insetBlockStart: { base: "4", sm: "6" },
  insetInlineEnd: { base: "4", sm: "6" },
});

export const page = css({
  minBlockSize: "viewport",
  display: "grid",
  placeItems: "center",
  paddingBlockStart: { base: "20", sm: "24" },
  paddingInline: { base: "7", sm: "6" },
  paddingBlockEnd: { base: "18", sm: "24" },
  bg: "surface",
  color: "fg",
  fontFamily: "ui",
  "& h1": {
    fontSize: "2xl",
    fontWeight: "semibold",
    lineHeight: "heading",
    letterSpacing: "heading",
    textWrap: "balance",
  },
  "& ::selection": { bg: "auth.selection", color: "fg" },
});

export const content = css({
  inlineSize: "full",
  maxInlineSize: "auth.content",
  minInlineSize: "0",
  display: "grid",
  gap: "6",
  overflowWrap: "anywhere",
  "& > div": { gap: "6" },
});

export const flow = css({ display: "grid", gap: "6" });
export const headingGroup = css({ display: "grid", gap: "2" });
export const formSection = css({
  display: "grid",
  gap: "7",
  minInlineSize: "0",
});
export const recoveryLink = css({
  fontSize: "secondary",
  fontWeight: "normal",
  minBlockSize: "6",
  display: "inline-flex",
  alignItems: "center",
});
export const secondaryActions = css({
  display: "grid",
  gap: "3",
  textAlign: "start",
  marginBlockStart: "1",
  color: "subtle",
  fontSize: "secondary",
});
export const secondaryLink = css({
  display: "inline-block",
  minBlockSize: "6",
  justifySelf: "start",
  textAlign: "start",
});
export const backLink = css({
  justifySelf: "center",
  fontSize: "secondary",
  minBlockSize: "6",
});
