import { css } from "../../../styled-system/css";

export const page = css({
  minInlineSize: "0",
  color: "fg",
  fontFamily: "ui",
  fontSize: "sm",
  lineHeight: "body",
});
export const header = css({
  display: "flex",
  flexWrap: "wrap",
  justifyContent: "space-between",
  alignItems: "start",
  gap: "4",
  marginBlockEnd: "6",
});
export const intro = css({ display: "grid", gap: "2", minInlineSize: "0" });
export const title = css({
  fontSize: "2xl",
  fontWeight: "semibold",
  lineHeight: "heading",
  letterSpacing: "heading",
  overflowWrap: "anywhere",
});
export const heading = css({
  fontSize: "lg",
  fontWeight: "semibold",
  lineHeight: "heading",
});
export const muted = css({ color: "subtle", overflowWrap: "anywhere" });
export const metadata = css({
  display: "flex",
  flexWrap: "wrap",
  columnGap: "4",
  rowGap: "1",
  color: "subtle",
  fontSize: "secondary",
});
export const note = css({
  backgroundColor: "action.ghostHover",
  color: "subtle",
  padding: "4",
  borderRadius: "control",
  fontSize: "secondary",
  marginBlockEnd: "5",
  overflowWrap: "anywhere",
});
export const recoveryTarget = css({ outline: "none" });
export const state = css({
  display: "grid",
  gap: "3",
  justifyItems: "start",
  paddingBlock: "8",
  maxInlineSize: "prose",
});
export const overview = css({ display: "grid", gap: "9" });
export const summary = css({
  display: "grid",
  gap: "8",
  gridTemplateColumns: {
    base: "minmax(0, 1fr)",
    lg: "repeat(2, minmax(0, 1fr))",
  },
});
export const summarySection = css({
  outline: "none",
  display: "grid",
  alignContent: "start",
  gap: "4",
  minInlineSize: "0",
});
export const facts = css({
  display: "grid",
  gridTemplateColumns: "auto minmax(0, 1fr)",
  columnGap: "5",
  rowGap: "2",
  "& dt": { color: "subtle" },
  "& dd": { margin: "0", overflowWrap: "anywhere" },
});
export const sectionHeader = css({
  display: "flex",
  alignItems: "center",
  justifyContent: "space-between",
  flexWrap: "wrap",
  gap: "3",
  marginBlockEnd: "4",
});
export const list = css({
  borderBlockStartWidth: "1px",
  borderBlockStartStyle: "solid",
  borderBlockStartColor: "separator",
});
export const service = css({
  display: "grid",
  gridTemplateColumns: {
    base: "minmax(0, 1fr)",
    md: "minmax(0, 1fr) minmax(0, 1fr) minmax(0, 1fr)",
  },
  alignItems: "center",
  gap: "3",
  paddingBlock: "5",
  borderBlockEndWidth: "1px",
  borderBlockEndStyle: "solid",
  borderBlockEndColor: "separator",
  overflowWrap: "anywhere",
});
export const serviceName = css({
  display: "flex",
  alignItems: "center",
  gap: "3",
  minInlineSize: "0",
  fontWeight: "medium",
  "--icon-size": "token(sizes.4)",
});
export const code = css({
  fontFamily: "mono",
  fontSize: "secondary",
  overflowWrap: "anywhere",
});
export const configuration = css({
  display: "grid",
  gap: "6",
  "--xy-attribution-background-color": "token(colors.surface)",
  // React Flow hardcodes this link's colour in its unlayered stylesheet.
  "& .react-flow__attribution a": { color: "subtle!" },
  "& > section": {
    borderWidth: "0",
    padding: "0",
    backgroundColor: "transparent",
  },
});
export const history = css({
  borderBlockStartWidth: "1px",
  borderBlockStartStyle: "solid",
  borderBlockStartColor: "separator",
  paddingBlockStart: "5",
  "& > section": { borderWidth: "0", padding: "0", marginBlockStart: "4" },
});

export const settings = css({ display: "grid", gap: "6" });

export const revisions = css({
  display: "grid",
  gap: "4",
  "& > div": { display: "grid", gap: "2", justifyItems: "start" },
  "& time": { color: "subtle", fontSize: "secondary" },
});
