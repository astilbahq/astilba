import { css } from "../styled-system/css";
export const shell = css({
  maxInlineSize: "shell",
  marginBlock: "0",
  marginInline: "auto",
  minBlockSize: "viewport",
  display: "grid",
  gridTemplateColumns: { base: "1fr", md: "224px 1fr" },
});
export const side = css({
  paddingBlock: "7",
  paddingInline: "5.5",
  borderInlineEndWidth: { md: "1px" },
  borderInlineEndStyle: "solid",
  borderInlineEndColor: "border",
  borderBlockEndWidth: { base: "1px", md: "0" },
  borderBlockEndStyle: "solid",
  borderBlockEndColor: "border",
  display: "flex",
  flexDirection: "column",
  gap: "7",
});
export const appearance = css({
  display: "flex",
  justifyContent: "flex-end",
  marginBlockEnd: "4",
});
export const main = css({
  paddingBlock: { base: "6", md: "10" },
  paddingInline: { base: "4.5", md: "12" },
  minInlineSize: "0",
});
export const stack = css({ display: "grid", gap: "5" });
export const row = css({
  display: "flex",
  alignItems: "center",
  gap: "3",
  flexWrap: "wrap",
});
export const spread = css({
  display: "flex",
  justifyContent: "space-between",
  alignItems: "center",
  gap: "4",
  flexWrap: "wrap",
});
export const panel = css({
  bg: "surface",
  borderWidth: "1px",
  borderStyle: "solid",
  borderColor: "border",
  borderRadius: "panel",
  padding: { base: "5", md: "7" },
  display: "grid",
  gap: "5",
  minInlineSize: "0",
});
export const muted = css({ color: "subtle", fontSize: "secondary" });
export const notice = css({
  bg: "soft",
  paddingBlock: "3.5",
  paddingInline: "4",
  borderRadius: "control",
  overflowWrap: "anywhere",
});
export const error = css({
  color: "error",
  borderWidth: "1px",
  borderStyle: "solid",
  borderColor: "current",
  paddingBlock: "3",
  paddingInline: "4",
  borderRadius: "control",
  overflowWrap: "anywhere",
});
export const columns = css({
  display: "grid",
  gridTemplateColumns: {
    base: "1fr",
    lg: "minmax(0, 1.7fr) minmax(260px, 1fr)",
  },
  gap: "6",
  alignItems: "start",
});
export const brand = css({
  fontSize: "brand",
  letterSpacing: "brand",
  fontWeight: "brand",
  textDecoration: "none",
  color: "fg",
});
export const nav = css({ display: "grid", gap: "2" });
export const code = css({
  fontFamily: "mono",
  fontSize: "xs",
  overflowWrap: "anywhere",
});
export const authenticatorQR = css({
  maxInlineSize: "full",
  blockSize: "auto",
});
export const editor = css({
  fontFamily: "mono",
  fontSize: "secondary",
  lineHeight: "editor",
  minBlockSize: "editor",
  tabSize: 2,
});
export const list = css({
  display: "grid",
  gap: "0",
  "& > *": {
    paddingBlock: "3",
    paddingInline: "0",
    borderBlockEndWidth: "1px",
    borderBlockEndStyle: "solid",
    borderBlockEndColor: "border",
  },
  "& > *:last-child": { borderBlockEndWidth: "0" },
});

export const screenReaderOnly = css({
  position: "absolute",
  inlineSize: "hairline",
  blockSize: "hairline",
  padding: "0",
  margin: "-hairline",
  overflow: "hidden",
  clip: "rect(0, 0, 0, 0)",
  whiteSpace: "nowrap",
  border: "none",
});

export const log = css({
  fontFamily: "mono",
  fontSize: "xs",
  whiteSpace: "pre-wrap",
  overflowWrap: "anywhere",
  maxBlockSize: "log",
  overflow: "auto",
});

export const metricSummary = css({
  display: "flex",
  flexWrap: "wrap",
  gap: "6",
  "& dt": { fontWeight: "semibold" },
  "& dd": { margin: "0" },
});
export const metricCharts = css({
  display: "grid",
  gridTemplateColumns: { base: "1fr", lg: "repeat(3, 1fr)" },
  gap: "4",
  "& figure": { margin: "0" },
});
export const metricChart = css({
  inlineSize: "full",
  blockSize: "chart",
  color: "brand",
});
export const metricTable = css({
  overflowInline: "auto",
  maxBlockSize: "table",
  "& table": { inlineSize: "full", borderCollapse: "collapse" },
  "& th, & td": {
    textAlign: "start",
    padding: "2",
    borderBlockEndWidth: "1px",
    borderBlockEndStyle: "solid",
    borderBlockEndColor: "border",
  },
});
