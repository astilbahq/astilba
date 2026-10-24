import { css } from "../styled-system/css";
export const shell = css({
  maxWidth: "1440px",
  margin: "0 auto",
  minHeight: "100vh",
  display: "grid",
  gridTemplateColumns: { base: "1fr", md: "224px 1fr" },
});
export const side = css({
  padding: "28px 22px",
  borderRight: { md: "1px solid token(colors.border)" },
  borderBottom: { base: "1px solid token(colors.border)", md: "none" },
  display: "flex",
  flexDirection: "column",
  gap: "28px",
});
export const main = css({
  padding: { base: "24px 18px", md: "40px 48px" },
  minWidth: 0,
});
export const stack = css({ display: "grid", gap: "20px" });
export const row = css({
  display: "flex",
  alignItems: "center",
  gap: "12px",
  flexWrap: "wrap",
});
export const spread = css({
  display: "flex",
  justifyContent: "space-between",
  alignItems: "center",
  gap: "16px",
  flexWrap: "wrap",
});
export const panel = css({
  bg: "surface",
  border: "1px solid token(colors.border)",
  borderRadius: "10px",
  padding: { base: "20px", md: "28px" },
  display: "grid",
  gap: "20px",
  minWidth: 0,
});
export const muted = css({ color: "subtle", fontSize: "13px" });
export const primary = css({
  bg: "brand!",
  color: { base: "white!", _dark: "#142b1c!" },
  borderColor: "brand!",
});
export const notice = css({
  bg: "soft",
  padding: "14px 16px",
  borderRadius: "6px",
  overflowWrap: "anywhere",
});
export const error = css({
  color: { base: "#982332", _dark: "#ffadb5" },
  border: "1px solid currentColor",
  padding: "12px 16px",
  borderRadius: "6px",
  overflowWrap: "anywhere",
});
export const columns = css({
  display: "grid",
  gridTemplateColumns: {
    base: "1fr",
    lg: "minmax(0, 1.7fr) minmax(260px, 1fr)",
  },
  gap: "24px",
  alignItems: "start",
});
export const brand = css({
  fontSize: "22px",
  letterSpacing: "-0.05em",
  fontWeight: 650,
  textDecoration: "none",
  color: "fg",
});
export const nav = css({
  display: "grid",
  gap: "8px",
  "& a": {
    padding: "8px 10px",
    borderRadius: "6px",
    textDecoration: "none",
    color: "subtle",
  },
  "& a[data-status=active]": { bg: "soft", color: "brand", fontWeight: 600 },
});
export const code = css({
  fontFamily: "ui-monospace, SFMono-Regular, monospace",
  fontSize: "12px",
  overflowWrap: "anywhere",
});
export const authenticatorQR = css({ maxWidth: "100%", height: "auto" });
export const editor = css({
  fontFamily: "ui-monospace, SFMono-Regular, monospace",
  fontSize: "13px",
  lineHeight: 1.7,
  minHeight: "360px",
  tabSize: 2,
});
export const list = css({
  display: "grid",
  gap: "0",
  "& > *": {
    padding: "12px 0",
    borderBottom: "1px solid token(colors.border)",
  },
  "& > *:last-child": { borderBottom: 0 },
});

export const screenReaderOnly = css({
  position: "absolute",
  width: "1px",
  height: "1px",
  padding: 0,
  margin: "-1px",
  overflow: "hidden",
  clip: "rect(0, 0, 0, 0)",
  whiteSpace: "nowrap",
  border: 0,
});

export const log = css({
  fontFamily: "ui-monospace, SFMono-Regular, monospace",
  fontSize: "12px",
  whiteSpace: "pre-wrap",
  overflowWrap: "anywhere",
  maxHeight: "280px",
  overflow: "auto",
});

export const metricSummary = css({
  display: "flex",
  flexWrap: "wrap",
  gap: "24px",
  "& dt": { fontWeight: 600 },
  "& dd": { margin: 0 },
});
export const metricCharts = css({
  display: "grid",
  gridTemplateColumns: { base: "1fr", lg: "repeat(3, 1fr)" },
  gap: "16px",
  "& figure": { margin: 0 },
});
export const metricChart = css({
  width: "100%",
  height: "100px",
  color: "brand",
});
export const metricTable = css({
  overflowX: "auto",
  maxHeight: "400px",
  "& table": { width: "100%", borderCollapse: "collapse" },
  "& th, & td": {
    textAlign: "left",
    padding: "8px",
    borderBottom: "1px solid token(colors.border)",
  },
});
