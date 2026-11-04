import { css } from "../../../styled-system/css";

export const intro = css({ display: "grid", gap: "1.5", minInlineSize: "0" });
export const parts = css({ display: "grid", gap: "8", minInlineSize: "0" });
export const part = css({ display: "grid", gap: "4", minInlineSize: "0" });
export const choice = css({
  display: "flex",
  alignItems: "flex-start",
  gap: "3",
});
export const focusTarget = css({ outline: "none" });
export const heading = css({
  fontSize: "md",
  fontWeight: "semibold",
  lineHeight: "heading",
  outline: "none",
});
export const session = css({
  display: "flex",
  flexWrap: "wrap",
  justifyContent: "space-between",
  alignItems: "start",
  gap: "3",
});
export const sessionDetails = css({
  display: "grid",
  gap: "1",
  minInlineSize: "0",
  overflowWrap: "anywhere",
});
export const metadata = css({
  display: "flex",
  flexWrap: "wrap",
  columnGap: "4",
  rowGap: "1",
  color: "subtle",
  fontSize: "secondary",
  fontWeight: "normal",
});
export const targetName = css({ display: "block", fontWeight: "medium" });
export const actions = css({ display: "flex", flexWrap: "wrap", gap: "3" });
export const help = css({ color: "subtle", fontSize: "secondary" });
export const nowrap = css({ whiteSpace: "nowrap" });
export const historyTable = css({
  overflowInline: "auto",
  "& table": { inlineSize: "full", borderCollapse: "collapse" },
  "& th, & td": {
    textAlign: "start",
    padding: "2",
    borderBlockEndWidth: "1px",
    borderBlockEndStyle: "solid",
    borderBlockEndColor: "border",
  },
  "& th": { fontWeight: "medium" },
});
